package response

import "time"

// R matches the response payload structure used by the app response package.
type R struct {
	Code int         `json:"code"`
	Msg  string      `json:"msg"`
	Ts   int64       `json:"ts"`
	Data interface{} `json:"data,omitempty"`
}

func Ok(data interface{}) R        { return R{Code: 0, Msg: "操作成功", Ts: time.Now().UnixMilli(), Data: data} }
func OkNoData() R                  { return R{Code: 0, Msg: "操作成功", Ts: time.Now().UnixMilli()} }
func OkMsg(msg string) R           { return R{Code: 0, Msg: msg, Ts: time.Now().UnixMilli()} }
func Err(code int, msg string) R   { return R{Code: code, Msg: msg, Ts: time.Now().UnixMilli()} }
func ErrMsg(msg string) R          { return Err(-1, msg) }
