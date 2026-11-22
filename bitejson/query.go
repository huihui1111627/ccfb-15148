package bitejson

// Query names the fields to extract. Paths are evaluated against every line;
// wildcard subscripts collect all matching elements into a list column.
type Query struct {
	paths []*Path
}

// NewQuery compiles a query from path strings.
func NewQuery(paths ...string) (*Query, error) {
	q := &Query{}
	for _, s := range paths {
		p, err := ParsePath(s)
		if err != nil {
			return nil, err
		}
		q.paths = append(q.paths, p)
	}
	return q, nil
}

// MustQuery compiles a query or panics; convenient for tests and demos.
func MustQuery(paths ...string) *Query {
	q, err := NewQuery(paths...)
	if err != nil {
		panic(err)
	}
	return q
}

// Paths returns the compiled paths.
func (q *Query) Paths() []*Path { return q.paths }

// valueKind is the resolved type of one extracted cell.
type valueKind uint8

const (
	cellNull valueKind = iota
	cellBool
	cellNumber
	cellString
	cellObject
	cellArray
)

func (k valueKind) String() string {
	switch k {
	case cellNull:
		return "null"
	case cellBool:
		return "boolean"
	case cellNumber:
		return "number"
	case cellString:
		return "string"
	case cellObject:
		return "object"
	case cellArray:
		return "array"
	}
	return "unknown"
}

// Value is one decoded cell. Only the field matching Kind is meaningful.
type Value struct {
	Kind    valueKind
	Bool    bool
	Num     float64
	Str     string
	IsInt   bool
	Int64   int64
	Uint64  uint64
	Present bool // false when the path did not exist or was null
}

// column accumulates the values for one path across lines.
type column struct {
	path   *Path
	values []Value
	// kind is the column's resolved type after unification, valid only when
	// uniform is true.
	uniform bool
	kind    valueKind
	// raw holds the JSON text for object/array cells.
	raw []string
}

// columnMode controls how a path's value is produced.
type columnMode uint8

const (
	// modeScalar yields the scalar at the path, or a list (for wildcards).
	modeScalar columnMode = iota
	// modeList yields a JSON array of every value matched by wildcards.
	modeList
)
