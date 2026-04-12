package jmespathx

import upstream "github.com/jmespath/go-jmespath"

// Search evaluates a JMESPath expression using the upstream enhanced engine.
func Search(expression string, data interface{}) (interface{}, error) {
	return upstream.Search(expression, data)
}
