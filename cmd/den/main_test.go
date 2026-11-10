package main

import (
	"flag"
	"reflect"
	"testing"
)

func TestParseInterspersed(t *testing.T) {
	cases := []struct {
		args []string
		n    int
		want []string
	}{
		{[]string{"reflect", "-n", "5"}, 5, []string{"reflect"}},
		{[]string{"-n", "2", "a", "b"}, 2, []string{"a", "b"}},
		{[]string{"a", "--", "-n", "9"}, 3, []string{"a", "-n", "9"}},
	}

	for _, c := range cases {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		n := fs.Int("n", 3, "")
		got, err := parse(fs, c.args)
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if *n != c.n || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: got %v n=%d, want %v n=%d", c.args, got, *n, c.want, c.n)
		}
	}
}
