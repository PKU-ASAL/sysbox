package value

type Type string

const (
	NullType   Type = "null"
	BoolType   Type = "bool"
	NumberType Type = "number"
	StringType Type = "string"
	ListType   Type = "list"
	ObjectType Type = "object"
)
