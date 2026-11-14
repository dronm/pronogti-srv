package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	terminalfa "github.com/dronm/goterminalfa"
	"github.com/dronm/goterminalfa/internal/protocol"
	"github.com/dronm/goterminalfa/internal/store"
)

func closeShiftTestRequest() Request {
	return Request{DeviceID: "kkt-1", Command: "closeShiftIfOpen", RequestID: "close-shift-1", Params: json.RawMessage(`{"cashierName":"Cashier","suppressPrint":1}`)}
}

func closeShiftTestApp(t *testing.T, profile string, state byte, failCode byte, failErr error) (*Service, *receiptTestTransport, *store.Store) {
	t.Helper()
	transport := &receiptTestTransport{state: state}
	transport.respond = func(_ context.Context, call receiptWireCall) ([]byte, error) {
		if call.Code == failCode {
			return nil, failErr
		}
		if call.Code == 0x2A {
			result := []byte{4, 0, 6, 0, 0, 0, 7, 0, 0, 0}
			if profile == "ffd1.2" {
				result = append(result, 0)
			}
			return result, nil
		}
		return receiptTestResponse(call.Code, state), nil
	}
	device := NewDevice("kkt-1", "serial", "550101046182", terminalfa.NewClient(transport), 2)
	device.ProtocolProfile = profile
	app, journal := testService(t, []*Device{device}, BuiltinCommands())
	return app, transport, journal
}

func TestCloseShiftIfOpenChecksStateAtomicallyAndReplays(t *testing.T) {
	for _, profile := range []string{"base", "ffd1.2"} {
		for _, state := range []byte{0, 1} {
			t.Run(fmt.Sprintf("%s/state=%d", profile, state), func(t *testing.T) {
				app, transport, journal := closeShiftTestApp(t, profile, state, 0, nil)
				req := closeShiftTestRequest()
				first := app.Execute(context.Background(), req)
				if first.HTTPStatus != 200 {
					t.Fatalf("close failed: %s", first.Body)
				}
				data := receiptReplyData(t, first)
				if data["alreadyClosed"] != (state == 0) || data["shiftNumber"] != float64(4) {
					t.Fatalf("wrong close data: %s", first.Body)
				}
				want := []byte{0x02, 0x20}
				if state == 1 {
					want = append(want, 0x29, 0x2F, 0x2A)
				}
				if !bytes.Equal(receiptCalls(transport), want) {
					t.Fatalf("commands %X want %X", receiptCalls(transport), want)
				}
				for _, call := range transport.calls {
					if call.Session != 1 {
						t.Fatal("shift check and close used separate sessions")
					}
					if call.Code == 0x29 && !bytes.Equal(call.Payload, []byte{1}) {
						t.Fatal("print suppression not encoded")
					}
				}
				second := app.Execute(context.Background(), req)
				if !second.Replayed || !bytes.Equal(first.Body, second.Body) || !bytes.Equal(receiptCalls(transport), want) {
					t.Fatal("retry executed closure twice")
				}
				record, err := journal.Get(context.Background(), req.DeviceID, req.RequestID)
				if err != nil || record.State != store.StateCompleted {
					t.Fatalf("close not journaled: %v %+v", err, record)
				}
			})
		}
	}
}

func TestCloseShiftUnknownStateAndInvalidInputNeverMutate(t *testing.T) {
	app, transport, _ := closeShiftTestApp(t, "ffd1.2", 2, 0, nil)
	reply := app.Execute(context.Background(), closeShiftTestRequest())
	if reply.HTTPStatus != 502 || !bytes.Equal(receiptCalls(transport), []byte{0x02, 0x20}) {
		t.Fatalf("unknown shift state mutated device: %s %X", reply.Body, receiptCalls(transport))
	}
	for _, raw := range []string{`{}`, `{"cashierName":"Cashier","suppressPrint":true}`, `{"cashierName":"Cashier","extra":1}`} {
		app, transport, _ := closeShiftTestApp(t, "ffd1.2", 1, 0, nil)
		req := closeShiftTestRequest()
		req.Params = json.RawMessage(raw)
		reply := app.Execute(context.Background(), req)
		if reply.HTTPStatus != 400 || len(receiptCalls(transport)) != 0 {
			t.Fatalf("invalid close reached hardware: %s", reply.Body)
		}
	}
}

func TestCloseShiftLostFinalizationRetainsUnknownJournal(t *testing.T) {
	for _, cause := range []error{io.ErrUnexpectedEOF, &protocol.DeviceError{Code: 0x10}} {
		t.Run(cause.Error(), func(t *testing.T) {
			app, transport, journal := closeShiftTestApp(t, "ffd1.2", 1, 0x2A, cause)
			req := closeShiftTestRequest()
			reply := app.Execute(context.Background(), req)
			data := receiptReplyData(t, reply)
			cleanup, ok := data["cleanup"].(map[string]any)
			if !ok || cleanup["succeeded"] != true || data["failure"] == nil {
				t.Fatalf("missing failure/cleanup details: %s", reply.Body)
			}
			wantUnknown := !errors.As(cause, new(*protocol.DeviceError))
			record, err := journal.Get(context.Background(), req.DeviceID, req.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			if wantUnknown && (reply.HTTPStatus != 409 || record.State != store.StateUnknown) {
				t.Fatalf("lost finalization not unknown: %s %+v", reply.Body, record)
			}
			if !wantUnknown && (reply.HTTPStatus != 502 || record.State != store.StateCompleted) {
				t.Fatalf("device rejection not definitive: %s %+v", reply.Body, record)
			}
			count := len(receiptCalls(transport))
			second := app.Execute(context.Background(), req)
			if !second.Replayed || len(receiptCalls(transport)) != count {
				t.Fatal("uncertain closure was repeated")
			}
		})
	}
}

func TestCloseShiftCannotBeNestedInOperation(t *testing.T) {
	app, transport, _ := closeShiftTestApp(t, "ffd1.2", 1, 0, nil)
	req := closeShiftTestRequest()
	req.Command = "executeOperation"
	req.Params = json.RawMessage(`{"commands":[{"cmd":"closeShiftIfOpen","params":{"cashierName":"Cashier"}}]}`)
	reply := app.Execute(context.Background(), req)
	if reply.HTTPStatus != 400 || len(receiptCalls(transport)) != 0 {
		t.Fatalf("nested closure accepted: %s", reply.Body)
	}
}
