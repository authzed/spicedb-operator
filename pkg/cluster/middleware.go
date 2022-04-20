package cluster

import (
	"context"
	"encoding/base64"
	"math/rand"

	"k8s.io/klog/v2"

	"github.com/authzed/spicedb-operator/pkg/libctrl"
	"github.com/authzed/spicedb-operator/pkg/libctrl/handler"
)

func NewSyncID(size uint8) string {
	buf := make([]byte, size)
	rand.Read(buf) // nolint
	str := base64.StdEncoding.EncodeToString(buf)
	return str[:size]
}

func SyncIDMiddleware(in handler.Handler) handler.Handler {
	return handler.NewHandlerFromFunc(func(ctx context.Context) {
		_, ok := ctxSyncID.Value(ctx)
		if !ok {
			ctx = ctxSyncID.WithValue(ctx, NewSyncID(5))
		}
		in.Handle(ctx)
	}, in.ID())
}

func KlogMiddleware(ref klog.ObjectRef) libctrl.HandlerMiddleware {
	return func(in handler.Handler) handler.Handler {
		return handler.NewHandlerFromFunc(func(ctx context.Context) {
			klog.V(4).InfoS("entering handler", "syncID", ctxSyncID.MustValue(ctx), "object", ref, "handler", in.ID())
			in.Handle(ctx)
		}, in.ID())
	}
}
