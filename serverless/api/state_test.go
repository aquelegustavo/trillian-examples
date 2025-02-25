// Copyright 2021 Google LLC. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// package api_test contains tests for the api package.
package api_test

import (
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/trillian-examples/serverless/api"
)

func TestNodeKey(t *testing.T) {
	for _, test := range []struct {
		level uint
		index uint64
		want  uint
	}{
		{
			level: 0,
			index: 0,
			want:  0,
		}, {
			level: 1,
			index: 0,
			want:  1,
		}, {
			level: 1,
			index: 1,
			want:  5,
		},
	} {
		t.Run(fmt.Sprintf("level %d, index %d", test.level, test.index), func(t *testing.T) {
			if got, want := api.TileNodeKey(test.level, test.index), test.want; got != want {
				t.Fatalf("got %d want %d", got, want)
			}
		})
	}
}

func emptyHashes(n uint) [][]byte {
	r := make([][]byte, n)
	for i := range r {
		r[i] = make([]byte, 32)
	}
	return r
}

func TestMarshalTileRoundtrip(t *testing.T) {
	for _, test := range []struct {
		size int
	}{
		{
			size: 1,
		}, {
			size: 256,
		}, {
			size: 11,
		}, {
			size: 42,
		},
	} {
		t.Run(fmt.Sprintf("tile size %d", test.size), func(t *testing.T) {
			tile := api.Tile{Nodes: make([][]byte, 0, test.size)}
			for i := 1; i < test.size; i++ {
				tile.NumLeaves = uint(i)
				idx := api.TileNodeKey(0, uint64(i-1))
				if l := uint(len(tile.Nodes)); idx >= l {
					tile.Nodes = append(tile.Nodes, emptyHashes(idx-l+1)...)
				}
				// Fill in the leaf index
				if _, err := rand.Read(tile.Nodes[idx]); err != nil {
					t.Error(err)
				}
			}

			raw, err := tile.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText() = %v", err)
			}

			tile2 := api.Tile{}
			if err := tile2.UnmarshalText(raw); err != nil {
				t.Fatalf("UnmarshalText() = %v", err)
			}

			if diff := cmp.Diff(tile, tile2); len(diff) != 0 {
				t.Fatalf("Got tile with diff: %s", diff)
			}
		})
	}
}
