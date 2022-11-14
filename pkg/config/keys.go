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

type intOrStringKey struct {
	key          string
	defaultValue int64
}

func newIntOrStringKey(key string, defaultValue int64) *intOrStringKey {
	return &intOrStringKey{
		key:          key,
		defaultValue: defaultValue,
	}
}

func (k *intOrStringKey) pop(config RawConfig) (out int64, err error) {
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
		err = fmt.Errorf("expected int or string for key %s", k.key)
	}
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

func (k metadataSetKey) pop(config RawConfig, metadataType string) (podMetadata map[string]string, warnings []error, err error) {
	v, ok := config[string(k)]
	delete(config, string(k))
	if !ok {
		return
	}

	podMetadata = make(map[string]string)

	switch value := v.(type) {
	case string:
		if len(value) > 0 {
			extraPodMetadataPairs := strings.Split(value, ",")
			for _, p := range extraPodMetadataPairs {
				k, v, ok := strings.Cut(p, "=")
				if !ok {
					warnings = append(warnings, fmt.Errorf("couldn't parse extra pod %s %q: values should be of the form k=v,k2=v2", metadataType, p))
					continue
				}
				podMetadata[k] = v
			}
		}
	case map[string]any:
		for k, v := range value {
			metadataValue, ok := v.(string)
			if !ok {
				warnings = append(warnings, fmt.Errorf("couldn't parse extra pod %s %v", metadataType, v))
				continue
			}
			podMetadata[k] = metadataValue
		}
	default:
		err = fmt.Errorf("expected string or map for key %s", k)
	}
	return
}
