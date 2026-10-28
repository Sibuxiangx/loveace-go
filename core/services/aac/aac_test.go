package aac

import (
	"context"
	"strings"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// ticketFlowTransport wires the CAS redirect chain that ends in a
// register?ticket= callback plus the AAC endpoints.
func ticketFlowTransport(t *testing.T) *transport.MockTransport {
	t.Helper()
	return testutil.LoginTransport(t).
		// Longer pattern wins over LoginTransport's bare "cas/login" GET
		// route: the AAC login service must 302 to the ticket callback.
		// The Location models the vpn2 gateway's REWRITTEN form — verified
		// live 2026-10-15: a healthy gateway session always rewrites the
		// CAS service redirect into the vpn2 namespace (an unrewritten raw
		// api.dekt.ac.acxk.net URL is the stale-session signature and
		// triggers session recovery instead).
		OnRequest(transport.Get, "/cas/login?service=http%3a%2f%2fapi.dekt", transport.NewResponse(302, []byte("moved")).
			WithHeader("Location", "http://api-dekt-ac-acxk-net.vpn2.aufe.edu.cn:8118/User/Index/CoreLoginCallback?isCASGateway=true&register?ticket=ST-AAC-1")).
		On("register?ticket=", transport.NewResponse(200, []byte("<html>callback</html>"))).
		OnRequest(transport.Post, "/User/Center/DoGetScoreInfo", transport.NewResponse(200, []byte(
			`{"code":0,"msg":"ok","data":{"TotalScore":2.5,"IsTypeAdopt":true,"TypeAdoptResult":"已认定"}}`))).
		OnRequest(transport.Post, "/User/Center/DoGetScoreList", transport.NewResponse(200, []byte(
			`{"code":0,"msg":"ok","data":[{"ID":"C1","ShowNum":1,"TypeName":"学科竞赛","TotalScore":2.5,"children":[{"ID":"C1-1","Title":"竞赛","TypeName":"国赛","UserNo":"20000002","Score":2.5,"AddTime":"2026-01-02"}]}]}`)))
}

func TestAacTicketExtractedFromRedirectChain(t *testing.T) {
	s := session.New("20000002", "gw", "school", ticketFlowTransport(t))
	sv := New(s)
	ticket, err := sv.Ticket(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if ticket != "ST-AAC-1" {
		t.Fatalf("ticket = %q", ticket)
	}
	// Cached on the second call.
	ticket2, err := sv.Ticket(context.Background(), false)
	if err != nil || ticket2 != ticket {
		t.Fatalf("cached ticket = %q %v", ticket2, err)
	}
	// Readiness output mirrors the CLI contract.
	if len(ticket) == 0 {
		t.Fatal("ticket_length must be > 0")
	}
}

func TestAacCreditInfoAndList(t *testing.T) {
	s := session.New("20000002", "gw", "school", ticketFlowTransport(t))
	sv := New(s)
	info, err := sv.CreditInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.TotalScore != 2.5 || !info.IsTypeAdopt || info.TypeAdoptResult != "已认定" {
		t.Fatalf("info = %+v", info)
	}
	list, err := sv.CreditList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "C1" || len(list[0].Children) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if list[0].Children[0].Score != 2.5 {
		t.Fatalf("child = %+v", list[0].Children[0])
	}
}

func TestAacServerCodeSurfaces(t *testing.T) {
	tr := testutil.LoginTransport(t).
		OnRequest(transport.Get, "cas/login?service=http%3a%2f%2fapi.dekt", transport.NewResponse(302, []byte("m")).
			WithHeader("Location", "http://api-dekt-ac-acxk-net.vpn2.aufe.edu.cn:8118/register?ticket=ST-9")).
		On("register?ticket=", transport.NewResponse(200, []byte("<html>callback</html>"))).
		OnRequest(transport.Post, "/User/Center/DoGetScoreInfo", transport.NewResponse(200, []byte(
			`{"code":401,"msg":"unauthorized","data":null}`)))
	s := session.New("20000002", "gw", "school", tr)
	sv := New(s)
	_, err := sv.CreditInfo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}
