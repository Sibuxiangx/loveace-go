package jwc

import (
	"context"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

func TestFreeClassroomCurrentSectionReadsTheTodayPage(t *testing.T) {
	tr := freeClassroomTransport(t).OnRequest(transport.Get, "/freeClassroom/today", transport.NewResponse(200, []byte(
		`<script>var sec = parseInt('3');</script>`)))
	section, err := freeClassroomService(tr).FreeClassroomCurrentSection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if section != 3 {
		t.Fatalf("section = %d, want 3", section)
	}
}

func TestFreeClassroomCurrentSectionOutOfRangeIsParseError(t *testing.T) {
	tr := freeClassroomTransport(t).OnRequest(transport.Get, "/freeClassroom/today", transport.NewResponse(200, []byte(
		`<script>var sec = parseInt('11');</script>`)))
	_, err := freeClassroomService(tr).FreeClassroomCurrentSection(context.Background())
	if errs.CodeOf(err) != errs.Parse {
		t.Fatalf("code = %v, want parse", errs.CodeOf(err))
	}
}
