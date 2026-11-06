package metadata

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	MaxGeneratedNameLength = validation.LabelValueMaxLength
	MaxFieldManagerLength  = 128

	nameHashLength = 8
)

func BoundedName(name string, maxLength int) string {
	if maxLength <= 0 {
		return ""
	}
	if len(name) <= maxLength {
		return name
	}

	hash := nameHash(name)
	if maxLength <= len(hash) {
		return hash[:maxLength]
	}

	prefix := strings.TrimRight(name[:maxLength-len(hash)-1], "-._")
	if prefix == "" {
		return hash
	}
	return prefix + "-" + hash
}

func nameHash(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])[:nameHashLength]
}
