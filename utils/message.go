package utils

// import (
// 	"bytes"
// 	"encoding/gob"

// 	"github.com/libp2p/go-libp2p/core/peer"
// 	"github.com/sirupsen/logrus"
// )

// // 消息结构体
// type Message struct {
// 	Type     string  // 消息类型
// 	Sender   peer.ID // 发送方ID
// 	Receiver peer.ID // 接收方ID
// }

// // 请求消息
// type RequestMessage struct {
// 	Message
// 	Payload []byte // 消息负载
// }

// // 响应消息
// type ResponseMessage struct {
// 	Message
// 	Code int    // 响应代码
// 	Msg  string // 响应消息
// 	Data []byte // 响应数据
// }

// // 序列化请求
// func (request *RequestMessage) SerializeRequest() ([]byte, error) {
// 	var buf bytes.Buffer
// 	enc := gob.NewEncoder(&buf)
// 	if err := enc.Encode(request); err != nil {

// 		logrus.Errorf("数据格式化失败%v", err)
// 		return nil, err
// 	}
// 	return buf.Bytes(), nil
// }

// // 反序列化请求
// func DeserializeRequest(data []byte) (*RequestMessage, error) {
// 	var request RequestMessage
// 	dec := gob.NewDecoder(bytes.NewReader(data))
// 	err := dec.Decode(&request)
// 	if err != nil {
// 		return nil, err
// 	}
// 	return &request, nil
// }

// // 序列化响应
// func (response *ResponseMessage) SerializeResponse() ([]byte, error) {
// 	var buf bytes.Buffer
// 	enc := gob.NewEncoder(&buf)
// 	if err := enc.Encode(response); err != nil {
// 		return nil, err
// 	}
// 	return buf.Bytes(), nil
// }

// // 反序列化响应
// func DeserializeResponse(data []byte) (*ResponseMessage, error) {
// 	var response ResponseMessage
// 	dec := gob.NewDecoder(bytes.NewReader(data))
// 	err := dec.Decode(&response)
// 	if err != nil {
// 		return nil, err
// 	}
// 	return &response, nil
// }
