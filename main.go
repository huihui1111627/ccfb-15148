package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"txmq/mq"
	"txmq/sim"
)

var consumerNames = []string{"inventory", "points", "logistics"}

func main() {
	var (
		orders     = flag.Int("orders", 300, "下单流量:订单总数")
		httpAddr   = flag.String("http", ":8080", "监控页面地址")
		dataDir    = flag.String("data", "data", "数据目录(MQ WAL / 订单库 / 消费记录)")
		mqAddr     = flag.String("mq", "127.0.0.1:9700", "MQ 服务端监听地址")
		crashProb  = flag.Float64("crash", 0.15, "每笔订单生产者崩溃概率")
		dropProb   = flag.Float64("drop", 0.03, "网络丢包概率(请求与响应各自)")
		dbFailProb = flag.Float64("dbfail", 0.05, "订单库提交失败概率")
		restarts   = flag.Int("restarts", 2, "仿真期间 MQ 服务端重启次数")
		maxChecks  = flag.Int("maxchecks", 6, "单条半消息最大回查次数")
		policy     = flag.String("policy", "rollback", "超过最大回查次数后的策略: rollback|commit")
		once       = flag.Bool("once", false, "验收完成后退出(不启动监控页面)")
		seed       = flag.Int64("seed", 42, "随机种子")
	)
	flag.Parse()

	os.RemoveAll(*dataDir)
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		panic(err)
	}

	rec := mq.NewRecorder()
	cfg := mq.Config{
		Addr:              *mqAddr,
		DataFile:          filepath.Join(*dataDir, "mq.wal"),
		CheckAfter:        1500 * time.Millisecond,
		CheckInterval:     400 * time.Millisecond,
		CheckCallTimeout:  800 * time.Millisecond,
		MaxChecks:         *maxChecks,
		Policy:            *policy,
		ExpectedConsumers: consumerNames,
		DeliverTimeout:    800 * time.Millisecond,
		FaultProb:         *dropProb,
	}

	var srvPtr atomic.Pointer[mq.Server]
	srv := mq.NewServer(cfg, rec)
	if err := srv.Start(); err != nil {
		panic(err)
	}
	srvPtr.Store(srv)
	fmt.Printf("[sim] MQ 服务端已启动 %s (WAL: %s)\n", *mqAddr, cfg.DataFile)

	// 订单库
	db, err := sim.OpenOrderDB(filepath.Join(*dataDir, "orders.db"), *dbFailProb, *seed)
	if err != nil {
		panic(err)
	}

	// 下游消费者:库存 / 积分 / 物流
	var consumers []*sim.Consumer
	for _, name := range consumerNames {
		c, err := sim.NewConsumer(name, filepath.Join(*dataDir, "received-"+name+".log"))
		if err != nil {
			panic(err)
		}
		cc := mq.NewConsumerClient(name, *mqAddr, c.Handle)
		cc.DropProb = *dropProb
		cc.RespDropProb = *dropProb
		cc.MaxLatency = 30 * time.Millisecond
		cc.Start()
		consumers = append(consumers, c)
	}
	fmt.Printf("[sim] 消费者已启动: %v\n", consumerNames)

	// 下单服务(生产者组)
	svc := sim.NewOrderService(db, "order-service", *mqAddr, *dropProb, *dropProb, *crashProb, *seed+1)

	// 服务端重启注入
	var simWG sync.WaitGroup
	simWG.Add(1)
	go func() {
		defer simWG.Done()
		rng := rand.New(rand.NewSource(*seed + 2))
		for i := 0; i < *restarts; i++ {
			time.Sleep(time.Duration(1500+rng.Intn(3000)) * time.Millisecond)
			fmt.Printf("[sim] !!! 注入 MQ 服务端崩溃重启 (%d/%d)\n", i+1, *restarts)
			old := srvPtr.Load()
			old.Stop()
			time.Sleep(600 * time.Millisecond) // 宕机窗口
			ns := mq.NewServer(cfg, rec)
			if err := ns.Start(); err != nil {
				panic(err)
			}
			srvPtr.Store(ns)
			atomic.AddInt64(&rec.Restarts, 1)
			fmt.Println("[sim] MQ 服务端已从 WAL 恢复并重新上线")
		}
	}()

	// 下单流量
	items := []string{"手机", "耳机", "键盘", "显示器", "鼠标", "音箱", "平板", "手表"}
	rng := rand.New(rand.NewSource(*seed + 3))
	fmt.Printf("[sim] 开始下单流量: %d 笔 (崩溃率 %.0f%%, 丢包率 %.0f%%, 订单库失败率 %.0f%%)\n",
		*orders, *crashProb*100, *dropProb*100, *dbFailProb*100)
	start := time.Now()
	outcomes := map[string]int{}
	for i := 0; i < *orders; i++ {
		o := sim.Order{
			ID:     fmt.Sprintf("ORD-%05d", i),
			Item:   items[rng.Intn(len(items))],
			Amount: 1 + rng.Intn(5),
		}
		outcome := svc.PlaceOrder(o)
		outcomes[outcome]++
		if (i+1)%50 == 0 {
			fmt.Printf("[sim] 已处理 %d/%d 笔订单\n", i+1, *orders)
		}
		time.Sleep(time.Duration(3+rng.Intn(10)) * time.Millisecond)
	}
	fmt.Printf("[sim] 下单结束,耗时 %s,结局分布: %v\n", time.Since(start).Round(time.Millisecond), outcomes)

	// 保证生产者组有存活实例应答回查
	svc.EnsureAlive()

	// 等待系统静默:所有半消息决议完毕,所有已提交消息投递完毕
	fmt.Println("[sim] 等待所有事务决议并投递完成...")
	deadline := time.Now().Add(90 * time.Second)
	for {
		if srvPtr.Load().Quiescent() {
			break
		}
		if time.Now().After(deadline) {
			fmt.Println("[sim] 警告:等待静默超时,按当前状态验收")
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	simWG.Wait()
	time.Sleep(500 * time.Millisecond)

	// 验收
	report := sim.Verify(db, srvPtr.Load(), consumers, rec, svc.Crashes)
	fmt.Println(report.String())

	if *once {
		if !report.Consistent {
			os.Exit(1)
		}
		return
	}

	fmt.Printf("[sim] 监控页面: http://localhost%s\n", *httpAddr)
	serveHTTP(*httpAddr, &srvPtr, rec, report, db, svc)
}
