/*
   Copyright The Soci Snapshotter Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package spanmanager

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"testing"

	"github.com/awslabs/soci-snapshotter/cache"
	"github.com/awslabs/soci-snapshotter/soci"
	"github.com/awslabs/soci-snapshotter/util/testutil"
)

func init() {
	rand.Seed(100)
}

func TestSpanManager(t *testing.T) {
	var spanSize soci.FileSize = 65536 // 64 KiB
	fileName := "span-manager-test"
	testCases := []struct {
		name          string
		maxSpans      soci.SpanId
		sectionReader *io.SectionReader
		expectedError error
	}{
		{
			name:     "a file from 1 span",
			maxSpans: 1,
		},
		{
			name:     "a file from 100 spans",
			maxSpans: 100,
		},
		{
			name:     "span digest verification fails",
			maxSpans: 100,
			sectionReader: io.NewSectionReader(readerFn(func(b []byte, _ int64) (int, error) {
				var sz soci.FileSize = soci.FileSize(len(b))
				copy(b, genRandomByteData(sz))
				return len(b), nil
			}), 0, 1000000),
			expectedError: ErrIncorrectSpanDigest,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			defer func() {
				if err != nil && !errors.Is(err, tc.expectedError) {
					t.Fatal(err)
				}
			}()

			fileContent := []byte{}
			for i := 0; i < int(tc.maxSpans); i++ {
				fileContent = append(fileContent, genRandomByteData(spanSize)...)
			}
			tarEntries := []testutil.TarEntry{
				testutil.File(fileName, string(fileContent)),
			}

			ztoc, r, err := soci.BuildZtocReader(tarEntries, gzip.BestCompression, int64(spanSize))
			if err != nil {
				err = fmt.Errorf("failed to create ztoc: %w", err)
				return
			}

			if tc.sectionReader != nil {
				r = tc.sectionReader
			}

			cache := cache.NewMemoryCache()
			defer cache.Close()
			m := New(ztoc, r, cache)

			// Test GetContent
			fileContentFromSpans, err := getFileContentFromSpans(m, ztoc, fileName)
			if err != nil {
				return
			}
			if !bytes.Equal(fileContent, fileContentFromSpans) {
				err = fmt.Errorf("file contents are not the same as span contents")
				return
			}

			// Test resolving all spans
			var i soci.SpanId
			for i = 0; i <= ztoc.MaxSpanId; i++ {
				err := m.ResolveSpan(i, r)
				if err != nil {
					t.Fatalf("error resolving span %d. error: %v", i, err)
				}
			}

			// Test ResolveSpan returning ErrExceedMaxSpan for span id larger than max span id
			resolveSpanErr := m.ResolveSpan(ztoc.MaxSpanId+1, r)
			if !errors.Is(resolveSpanErr, ErrExceedMaxSpan) {
				t.Fatalf("failed returning ErrExceedMaxSpan for span id larger than max span id")
			}
		})
	}
}

func TestSpanManagerCache(t *testing.T) {
	var spanSize soci.FileSize = 65536 // 64 KiB
	content := genRandomByteData(spanSize)
	tarEntries := []testutil.TarEntry{
		testutil.File("span-manager-cache-test", string(content)),
	}
	ztoc, r, err := soci.BuildZtocReader(tarEntries, gzip.BestCompression, int64(spanSize))
	if err != nil {
		t.Fatalf("failed to create ztoc: %v", err)
	}
	cache := cache.NewMemoryCache()
	defer cache.Close()
	m := New(ztoc, r, cache)
	m.addSpanToCache("spanId", content)

	testCases := []struct {
		name   string
		offset soci.FileSize
	}{
		{
			name:   "offset 0",
			offset: 0,
		},
		{
			name:   "offset 20000",
			offset: 20000,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			buf := make([]byte, spanSize-tc.offset)
			r, err := m.getSpanFromCache("spanId", tc.offset, spanSize-tc.offset)
			if err != nil {
				t.Fatalf("error getting span content from cache")
			}
			_, err = r.Read(buf)
			if err != nil && err != io.EOF {
				t.Fatalf("error reading span content")
			}
			if !bytes.Equal(buf, content[tc.offset:]) {
				t.Fatalf("span content from cache is not expected")
			}
		})
	}
}

func TestStateTransition(t *testing.T) {
	var spanSize soci.FileSize = 65536 // 64 KiB
	content := genRandomByteData(spanSize)
	tarEntries := []testutil.TarEntry{
		testutil.File("set-span-test", string(content)),
	}
	ztoc, r, err := soci.BuildZtocReader(tarEntries, gzip.BestCompression, int64(spanSize))
	if err != nil {
		t.Fatalf("failed to create ztoc: %v", err)
	}
	cache := cache.NewMemoryCache()
	defer cache.Close()
	m := New(ztoc, r, cache)

	// check initial span states
	for i := uint32(0); i <= uint32(ztoc.MaxSpanId); i++ {
		state := m.spans[i].state.Load().(spanState)
		if state != unrequested {
			t.Fatalf("failed initializing span states to Unrequested")
		}
	}

	testCases := []struct {
		name       string
		spanID     soci.SpanId
		isPrefetch bool
	}{
		{
			name:       "span 0 - prefetch",
			spanID:     0,
			isPrefetch: true,
		},
		{
			name:       "span 0 - on demand fetch",
			spanID:     0,
			isPrefetch: false,
		},
		{
			name:       "max span - prefetch",
			spanID:     m.ztoc.MaxSpanId,
			isPrefetch: true,
		},
		{
			name:       "max span - on demand fetch",
			spanID:     m.ztoc.MaxSpanId,
			isPrefetch: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := m.spans[tc.spanID]
			if tc.isPrefetch {
				err := m.ResolveSpan(tc.spanID, r)
				if err != nil {
					t.Fatalf("failed resolving the span for prefetch")
				}
				state := s.state.Load().(spanState)
				if state != fetched {
					t.Fatalf("failed transitioning to Fetched state")
				}
			} else {
				_, err := m.GetSpanContent(tc.spanID, 0, s.endUncompOffset, s.endUncompOffset-s.startUncompOffset)
				if err != nil {
					t.Fatalf("failed getting the span for on-demand fetch")
				}
				state := s.state.Load().(spanState)
				if state != uncompressed {
					t.Fatalf("failed transitioning to Uncompressed state")
				}
			}
		})
	}
}

func TestValidateState(t *testing.T) {
	testCases := []struct {
		name         string
		currentState spanState
		newState     []spanState
		expectedErr  error
	}{
		{
			name:         "span in Unrequested state with valid new state",
			currentState: unrequested,
			newState:     []spanState{unrequested, requested},
			expectedErr:  nil,
		},
		{
			name:         "span in Unrequested state with invalid new state",
			currentState: unrequested,
			newState:     []spanState{fetched},
			expectedErr:  errInvalidSpanStateTransition,
		},
		{
			name:         "span in Requested state with valid new state",
			currentState: requested,
			newState:     []spanState{requested, fetched},
			expectedErr:  nil,
		},
		{
			name:         "span in Requested state with invalid new state",
			currentState: requested,
			newState:     []spanState{unrequested},
			expectedErr:  errInvalidSpanStateTransition,
		},
		{
			name:         "span in Fetched state with valid new state",
			currentState: fetched,
			newState:     []spanState{uncompressed, fetched},
			expectedErr:  nil,
		},
		{
			name:         "span in Fetched state with invalid new state",
			currentState: fetched,
			newState:     []spanState{unrequested},
			expectedErr:  errInvalidSpanStateTransition,
		},
		{
			name:         "span in Uncompressed state with valid new state",
			currentState: uncompressed,
			newState:     []spanState{uncompressed},
			expectedErr:  nil,
		},
		{
			name:         "span in Uncompressed state with invalid new state",
			currentState: uncompressed,
			newState:     []spanState{fetched},
			expectedErr:  errInvalidSpanStateTransition,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			for _, ns := range tc.newState {
				s := span{}
				s.state.Store(tc.currentState)
				err := s.validateStateTransition(ns)
				if !errors.Is(err, tc.expectedErr) {
					t.Fatalf("failed validateState")
				}
			}
		})
	}
}

func getFileContentFromSpans(m *SpanManager, ztoc *soci.Ztoc, fileName string) ([]byte, error) {
	metadata, err := soci.GetMetadataEntry(ztoc, fileName)
	if err != nil {
		return nil, err
	}
	offsetStart := metadata.UncompressedOffset
	offsetEnd := offsetStart + metadata.UncompressedSize
	r, err := m.GetContents(offsetStart, offsetEnd)
	if err != nil {
		return nil, err
	}
	content, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return content, nil
}

func genRandomByteData(size soci.FileSize) []byte {
	b := make([]byte, size)
	rand.Read(b)
	return b
}

type readerFn func([]byte, int64) (int, error)

func (f readerFn) ReadAt(b []byte, n int64) (int, error) {
	return f(b, n)
}
