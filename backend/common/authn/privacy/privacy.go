package privacy

import (
	"fmt"
	"reflect"

	"entgo.io/ent/dialect/sql"
)

type Predicate func(*sql.Selector)

func Where(queryOrMutation any, predicate *sql.Predicate) error {
	v := reflect.ValueOf(queryOrMutation)
	method := v.MethodByName("Where")
	if !method.IsValid() {
		return fmt.Errorf("method Where not found on %T", queryOrMutation)
	}

	selector := func(selector *sql.Selector) {
		selector.Where(predicate)
	}

	args := []reflect.Value{reflect.ValueOf(selector)}
	method.Call(args)

	return nil
}
