package movies

import (
	"reflect"
	"testing"
)

func TestIntArray_Scan(t *testing.T) {
	tests := []struct {
		name string
		src  any
		want IntArray
	}{
		{name: "text", src: "{18,53,9648}", want: IntArray{18, 53, 9648}},
		{name: "bytes", src: []byte("{825}"), want: IntArray{825}},
		{name: "empty", src: "{}", want: IntArray{}},
		{name: "null", src: nil, want: nil},
		{name: "negative", src: "{-1,2}", want: IntArray{-1, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got IntArray
			if err := got.Scan(tt.src); err != nil {
				t.Fatalf("Scan(%v): %v", tt.src, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Scan(%v) = %#v, want %#v", tt.src, got, tt.want)
			}
		})
	}
}

func TestIntArray_ScanRejectsMalformed(t *testing.T) {
	for _, src := range []any{"18,53", "{18,x}", "{1,NULL}", 42} {
		var got IntArray
		if err := got.Scan(src); err == nil {
			t.Errorf("Scan(%v) = %v, want an error", src, got)
		}
	}
}
