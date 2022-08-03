package object

import (
	"fmt"
	"strconv"
	"strings"
)

// RawObject has not been processed/validated yet
type RawObject map[string]any

// Key gives the name of a string key in a RawObject a typed accessor
type Key[V comparable] struct {
	key          string
	defaultValue V
}

// NewKey makes a new Key with a default value
func NewKey[V comparable](k string, defaultValue V) *Key[V] {
	return &Key[V]{
		key:          k,
		defaultValue: defaultValue,
	}
}

// NewStringKey is a shorthand for NewKey[string](key, "")
func NewStringKey(key string) *Key[string] {
	return NewKey(key, "")
}

// Peek returns the value defined by the Key
func (k *Key[V]) Peek(config RawObject) any {
	v, ok := config[k.key]
	if !ok {
		return k.defaultValue
	}
	return v
}

// Pop returns the value defined by the Key and deletes the key from the
// underlying RawObject.
func (k *Key[V]) Pop(config RawObject) V {
	v := k.Peek(config)
	delete(config, k.key)
	tv, ok := v.(V)
	if !ok {
		return k.defaultValue
	}

	return tv
}

// Key returns the string key value
func (k *Key[V]) Key() string {
	return k.key
}

// IntOrStringKey is like Key but the value can be an int or a string
// representation of an int.
type IntOrStringKey struct {
	key          string
	defaultValue int64
}

// NewIntOrStringKey returns a new IntOrStringKey with a default value
func NewIntOrStringKey(key string, defaultValue int64) *IntOrStringKey {
	return &IntOrStringKey{
		key:          key,
		defaultValue: defaultValue,
	}
}

// Pop returns the value defined by the key and removes it from the underlying
// RawObject
func (k *IntOrStringKey) Pop(config RawObject) (out int64, err error) {
	v, ok := config[k.key]
	delete(config, k.key)
	if !ok {
		return k.defaultValue, nil
	}

	switch value := v.(type) {
	case string:
		out, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return
		}
	case float64:
		out = int64(value)
	default:
		err = fmt.Errorf("expected int or string for Key %s", k.key)
	}
	return
}

// BoolOrStringKey is like Key but the value can be an bool or a string
// representation of an bool.
type BoolOrStringKey struct {
	key          string
	defaultValue bool
}

// NewBoolOrStringKey returns a new BoolOrStringKey with a default value
func NewBoolOrStringKey(key string, defaultValue bool) *BoolOrStringKey {
	return &BoolOrStringKey{
		key:          key,
		defaultValue: defaultValue,
	}
}

// Pop returns the value defined by the key and removes it from the underlying
// RawObject
func (k *BoolOrStringKey) Pop(config RawObject) (out bool, err error) {
	v, ok := config[k.key]
	delete(config, k.key)
	if !ok {
		return k.defaultValue, nil
	}

	switch value := v.(type) {
	case string:
		out, err = strconv.ParseBool(value)
		if err != nil {
			return
		}
	case bool:
		out = value
	default:
		err = fmt.Errorf("expected bool or string for Key %s", k.key)
	}
	return
}

// LabelSetKey is like Key but the value can either be a string representation
// of a set of labels (a=b,c=d), or a map containing keys and values.
type LabelSetKey string

// Pop returns the value defined by the key and removes it from the underlying
// RawObject
func (k LabelSetKey) Pop(config RawObject) (podLabels map[string]string, warnings []error, err error) {
	v, ok := config[string(k)]
	delete(config, string(k))
	if !ok {
		return
	}

	podLabels = make(map[string]string)

	switch value := v.(type) {
	case string:
		if len(value) > 0 {
			labelPairs := strings.Split(value, ",")
			for _, p := range labelPairs {
				k, v, ok := strings.Cut(p, "=")
				if !ok {
					warnings = append(warnings, fmt.Errorf("couldn't parse label %q: labels should be of the form k=v,k2=v2", p))
					continue
				}
				podLabels[k] = v
			}
		}
	case map[string]any:
		for k, v := range value {
			labelValue, ok := v.(string)
			if !ok {
				warnings = append(warnings, fmt.Errorf("couldn't parse label %v", v))
				continue
			}
			podLabels[k] = labelValue
		}
	default:
		err = fmt.Errorf("expected string or map for Key %s", k)
	}
	return
}
