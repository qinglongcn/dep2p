package internal

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/multiformats/go-multibase"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// StartSpan 函数用于启动一个跨度（span）。
// 它接收一个上下文（context.Context）对象、一个跨度名称（name）和一些跨度启动选项（opts）作为输入参数。
// 返回更新后的上下文对象和一个跨度对象。
func StartSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return otel.Tracer("go-libp2p-kad-dht").Start(ctx, fmt.Sprintf("KademliaDHT.%s", name), opts...)
}

// KeyAsAttribute 函数将 DHT 键格式化为适合跟踪的属性。
// DHT 键可以是有效的 UTF-8 字符串，也可以是二进制数据，例如从 Multihash 导出的键。
// 跟踪（特别是 OpenTelemetry+grpc 导出器）要求字符串属性使用有效的 UTF-8 字符串。
func KeyAsAttribute(name string, key string) attribute.KeyValue {
	b := []byte(key)
	// 检查键是否是有效的 UTF-8 字符串
	if utf8.Valid(b) {
		// 键是有效的 UTF-8 字符串，返回相应的属性
		return attribute.String(name, key)
	}
	// 对键进行 Base58 编码
	encoded, err := multibase.Encode(multibase.Base58BTC, b)
	if err != nil {
		// 不应该执行到这里
		panic(err)
	}
	// 返回编码后的属性
	return attribute.String(name, encoded)
}
