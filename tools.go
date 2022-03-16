//go:build tools
// +build tools

package tools

import (
	_ "k8s.io/code-generator/cmd/client-gen"
	_ "mvdan.cc/gofumpt"
	_ "sigs.k8s.io/controller-tools/cmd/controller-gen"
)
