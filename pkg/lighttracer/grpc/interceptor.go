package grpctrace

import (
	"context"
	"encoding/json"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/opentracing/opentracing-go/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/grpclog"
	"google.golang.org/grpc/metadata"
	"goweb/pkg/lighttracer"
	"goweb/pkg/lighttracer/tags"
	"strings"
)

//MDReaderWriter metadata Reader and Writer
type MDReaderWriter struct {
	metadata.MD
}

// ForeachKey implements ForeachKey of opentracing.TextMapReader
func (c MDReaderWriter) ForeachKey(handler func(key, val string) error) error {
	for k, vs := range c.MD {
		for _, v := range vs {
			if err := handler(k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// Set implements Set() of opentracing.TextMapWriter
func (c MDReaderWriter) Set(key, val string) {
	key = strings.ToLower(key)
	c.MD[key] = append(c.MD[key], val)
}

// DialOption grpc client option
func DialOption(tracer lighttracer.Tracer) grpc.DialOption {
	return grpc.WithUnaryInterceptor(ClientInterceptor(tracer))
}

// ServerOption grpc server option
func ServerOption(tracer lighttracer.Tracer) grpc.ServerOption {
	return grpc.UnaryInterceptor(ServerInterceptor(tracer))
}

// ClientInterceptor grpc client wrapper
func ClientInterceptor(tracer lighttracer.Tracer) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string,
		req, reply interface{}, cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {

		var parentCtx opentracing.SpanContext
		parentSpan := opentracing.SpanFromContext(ctx)
		if parentSpan != nil {
			parentCtx = parentSpan.Context()
		}

		span := tracer.StartSpanWithType(
			method,
			lighttracer.OperationTypeRPC,
			opentracing.ChildOf(parentCtx),
			opentracing.Tag{Key: string(ext.Component), Value: "gRPC"},
			ext.SpanKindRPCClient,
		)
		defer span.Finish()

		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok {
			md = metadata.New(nil)
		} else {
			md = md.Copy()
		}

		mdWriter := MDReaderWriter{md}
		err := tracer.Inject(span.Context(), opentracing.TextMap, mdWriter)
		if err != nil {
			span.LogFields(log.String("inject-error", err.Error()))
		}

		newCtx := metadata.NewOutgoingContext(ctx, md)
		err = invoker(newCtx, method, req, reply, cc, opts...)
		if err != nil {
			span.LogFields(log.String("call-error", err.Error()))
		}

		if bs, err := json.Marshal(req); err == nil {
			tags.RPCRequestBody.Set(span, string(bs))
		}

		if bs, err := json.Marshal(reply); err == nil {
			tags.RPCResponseBody.Set(span, string(bs))
		}

		return err
	}
}

// ServerInterceptor grpc server wrapper
func ServerInterceptor(tracer lighttracer.Tracer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (resp interface{}, err error) {

		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			md = metadata.New(nil)
		}

		spanContext, err := tracer.Extract(opentracing.TextMap, MDReaderWriter{md})
		if err != nil && err != opentracing.ErrSpanContextNotFound {
			grpclog.Errorf("extract from metadata err: %v", err)
		}

		span := tracer.StartSpanWithType(
			info.FullMethod,
			lighttracer.OperationTypeRPC,
			ext.RPCServerOption(spanContext),
			opentracing.Tag{Key: string(ext.Component), Value: "gRPC"},
			ext.SpanKindRPCServer,
		)
		defer span.Finish()

		ctx = opentracing.ContextWithSpan(ctx, span)

		resp, err = handler(ctx, req)

		if bs, err := json.Marshal(req); err == nil {
			tags.RPCRequestBody.Set(span, string(bs))
		}

		if bs, err := json.Marshal(resp); err == nil {
			tags.RPCResponseBody.Set(span, string(bs))
		}

		return
	}
}
