package util

import (
	"crypto/sha256"
	"fmt"

	"github.com/davecgh/go-spew/spew"
	"k8s.io/apimachinery/pkg/util/rand"
)

func SecureHashObject(obj interface{}) (string, error) {
	// using sha256 since the database uri may contain a secret
	hasher := sha256.New()
	printer := spew.ConfigState{
		Indent:         " ",
		SortKeys:       true,
		DisableMethods: true,
		SpewKeys:       true,
	}
	_, err := printer.Fprintf(hasher, "%#v", obj)
	if err != nil {
		return "", err
	}
	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum(nil))), nil
}
