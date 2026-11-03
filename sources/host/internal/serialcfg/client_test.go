package serialcfg

import (
	"testing"

	"github.com/jjundot/EspKVM/internal/protocol/pb"
	"google.golang.org/protobuf/proto"
)

func TestDeviceLogFramesNeverAnswerRequests(t *testing.T) {
	body, _ := proto.Marshal(&pb.SerialPacket{ProtoVer: 2, Body: &pb.SerialPacket_Log{Log: &pb.SerialLog{Text: "I (12) espkvm: boot"}}})
	if text, ok := logText(body); !ok || text != "I (12) espkvm: boot" {
		t.Fatal("log frame not recognized", text)
	}
	ack, _ := proto.Marshal(&pb.SerialPacket{ProtoVer: 2, Body: &pb.SerialPacket_Ack{Ack: &pb.SerialAck{}}})
	if _, ok := logText(ack); ok {
		t.Fatal("ack treated as log")
	}
	old, _ := proto.Marshal(&pb.SerialPacket{ProtoVer: 1, Body: &pb.SerialPacket_Log{Log: &pb.SerialLog{Text: "x"}}})
	if _, ok := logText(old); ok {
		t.Fatal("wrong protocol version accepted")
	}
}
