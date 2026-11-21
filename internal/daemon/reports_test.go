package daemon

import (
	"reflect"
	"testing"
)

func TestReportsWaitForTheSessionsLastQuestion(t *testing.T) {
	r := reports{open: map[string]int{}, held: map[string][]report{}}
	if r.keep("s1", "free", "") {
		t.Fatal("a session with nothing open kept its report")
	}

	r.add("s1")
	r.add("s1")
	r.add("s2")
	if !r.keep("s1", "first", "1") {
		t.Fatal("a session with questions open did not keep its report")
	}
	if held := r.done("s1"); held != nil {
		t.Fatalf("one question left open, yet done returned %v", held)
	}
	r.keep("s1", "second", "")
	r.keep("s2", "other", "")

	want := []report{{"first", "1"}, {"second", ""}}
	if held := r.done("s1"); !reflect.DeepEqual(held, want) {
		t.Fatalf("done returned %v, want %v", held, want)
	}
	if r.keep("s1", "late", "") {
		t.Fatal("a settled session kept a report")
	}
	if held := r.done("s2"); len(held) != 1 {
		t.Fatalf("s2 got %v", held)
	}
}
