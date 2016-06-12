package orc

import (
	"fmt"
	"testing"
)

func TestReader(t *testing.T) {

	r, err := Open("./examples/orc-file-11-format.orc")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	c := r.Select("boolean1", "int1", "string1", "byte1")

	for c.Stripes() {

		for c.Next() {

			fmt.Println(c.Row())

		}

	}

	if err := c.Err(); err != nil {
		t.Fatal(err)
	}

}
