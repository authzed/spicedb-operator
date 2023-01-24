package config

import (
	"fmt"
	"strconv"
	"strings"
)

func newKey[V comparable](k string, defaultValue V) *key[V] {
	return &key[V]{
		key:          k,
		defaultValue: defaultValue,
	}
}

func newStringKey(key string) *key[string] {
	return newKey(key, "")
}

func (k *key[V]) peek(config RawConfig) any {
	v, ok := config[k.key]
	if !ok {
		return k.defaultValue
	}
	return v
}

func (k *key[V]) pop(config RawConfig) V {
	v := k.peek(config)
	delete(config, k.key)
	tv, ok := v.(V)
	if !ok {
		return k.defaultValue
	}

	return tv
}

type intOrStringKey[I int64 | int32 | int16 | int8] struct {
	key          string
	defaultValue I
}

func newIntOrStringKey[I int64 | int32 | int16 | int8](key string, defaultValue I) *intOrStringKey[I] {
	return &intOrStringKey[I]{
		key:          key,
		defaultValue: defaultValue,
	}
}

func (k *intOrStringKey[I]) pop(config RawConfig) (out I, err error) {
	v, ok := config[k.key]
	delete(config, k.key)
	if !ok {
		return k.defaultValue, nil
	}

	var bits int
	switch any(out).(type) {
	case int8:
		bits = 8
	case int16:
		bits = 16
	case int32:
		bits = 32
	case int64:
		bits = 64
	default:
		panic("invalid int type")
	}

	var parsed int64
	parsed, err = strconv.ParseInt(fmt.Sprint(v), 10, bits)
	if err != nil {
		return
	}
	out = I(parsed)
	return
}

type boolOrStringKey struct {
	key          string
	defaultValue bool
}

func newBoolOrStringKey(key string, defaultValue bool) *boolOrStringKey {
	return &boolOrStringKey{
		key:          key,
		defaultValue: defaultValue,
	}
}

func (k *boolOrStringKey) pop(config RawConfig) (out bool, err error) {
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
		err = fmt.Errorf("expected bool or string for key %s", k.key)
	}
	return
}

type metadataSetKey string

func (k metadataSetKey) pop(config RawConfig, objectType, metadataType string) (metadata map[string]string, warnings []error, err error) {
	v, ok := config[string(k)]
	delete(config, string(k))
	if !ok {
		return
	}

	metadata = make(map[string]string)

	switch value := v.(type) {
	case string:
		if len(value) > 0 {
			extraMetadataPairs := strings.Split(value, ",")
			for _, p := range extraMetadataPairs {
				k, v, ok := strings.Cut(p, "=")
				if !ok {
					warnings = append(warnings, fmt.Errorf("couldn't parse extra %s %s %q: values should be of the form k=v,k2=v2", objectType, metadataType, p))
					continue
				}
				metadata[k] = v
			}
		}
	case map[string]any:
		for k, v := range value {
			metadataValue, ok := v.(string)
			if !ok {
				warnings = append(warnings, fmt.Errorf("couldn't parse extra %s %s %v", objectType, metadataType, v))
				continue
			}
			metadata[k] = metadataValue
		}
	default:
		err = fmt.Errorf("expected string or map for key %s", k)
	}
	return
}
