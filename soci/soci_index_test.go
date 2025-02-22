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

package soci

import (
	"context"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"
)

func TestSkipBuildingZtoc(t *testing.T) {
	testcases := []struct {
		name        string
		desc        ocispec.Descriptor
		buildConfig buildConfig
		skip        bool
	}{
		{
			name: "skip, size<minLayerSize",
			desc: ocispec.Descriptor{
				MediaType: SociLayerMediaType,
				Digest:    parseDigest("sha256:88a7002d88ed7b174259637a08a2ef9b7f4f2a314dfb51fa1a4a6a1d7e05dd01"),
				Size:      5223,
			},
			buildConfig: buildConfig{
				minLayerSize: 65535,
			},
			skip: true,
		},
		{
			name: "do not skip, size=minLayerSize",
			desc: ocispec.Descriptor{
				MediaType: SociLayerMediaType,
				Digest:    parseDigest("sha256:88a7002d88ed7b174259637a08a2ef9b7f4f2a314dfb51fa1a4a6a1d7e05dd01"),
				Size:      65535,
			},
			buildConfig: buildConfig{
				minLayerSize: 65535,
			},
			skip: false,
		},
		{
			name: "do not skip, size>minLayerSize",
			desc: ocispec.Descriptor{
				MediaType: SociLayerMediaType,
				Digest:    parseDigest("sha256:88a7002d88ed7b174259637a08a2ef9b7f4f2a314dfb51fa1a4a6a1d7e05dd01"),
				Size:      5000,
			},
			buildConfig: buildConfig{
				minLayerSize: 500,
			},
			skip: false,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			if skipBuildingZtoc(tc.desc, &tc.buildConfig) != tc.skip {
				t.Fatalf("%v: the value returned does not equal actual value %v", tc.name, tc.skip)
			}
		})
	}
}

func TestBuildSociIndexNotLayer(t *testing.T) {
	testcases := []struct {
		name          string
		mediaType     string
		errorNotLayer bool
	}{
		{
			name:          "empty media type",
			mediaType:     "",
			errorNotLayer: true,
		},
		{
			name:          "soci index manifest",
			mediaType:     sociIndexMediaType,
			errorNotLayer: true,
		},
		{
			name:          "soci layer",
			mediaType:     SociLayerMediaType,
			errorNotLayer: true,
		},
		{
			name:          "index manifest",
			mediaType:     "application/vnd.oci.image.manifest.v1+json",
			errorNotLayer: true,
		},
		{
			name:          "layer as tar",
			mediaType:     "application/vnd.oci.image.layer.v1.tar",
			errorNotLayer: false,
		},
		{
			name:          "layer as tar+gzip",
			mediaType:     "application/vnd.oci.image.layer.v1.tar+gzip",
			errorNotLayer: false,
		},
		{
			name:          "layer as tar+zstd",
			mediaType:     "application/vnd.oci.image.layer.v1.tar+zstd",
			errorNotLayer: false,
		},
		{
			name:          "layer prefix",
			mediaType:     "application/vnd.oci.image.layer.",
			errorNotLayer: false,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cs := newFakeContentStore()
			desc := ocispec.Descriptor{
				MediaType: tc.mediaType,
			}
			cfg := &buildConfig{}
			spanSize := int64(65535)
			blobStore := memory.New()
			_, err := buildSociLayer(ctx, cs, desc, spanSize, blobStore, cfg)
			if tc.errorNotLayer {
				if err != errNotLayerType {
					t.Fatalf("%v: should error out as not a layer", tc.name)
				}
			} else {
				if err == errNotLayerType {
					t.Fatalf("%v: should not error out for any of the layer types", tc.name)
				}
			}
		})
	}
}

func TestBuildSociIndexWithLimits(t *testing.T) {
	testcases := []struct {
		name          string
		layerSize     int64
		minLayerSize  int64
		ztocGenerated bool
	}{
		{
			name:          "skip building ztoc: layer size 500 bytes, minimal layer size 32kB",
			layerSize:     500,
			minLayerSize:  32000,
			ztocGenerated: false,
		},
		{
			name:          "skip building ztoc: layer size 20kB, minimal layer size 32kB",
			layerSize:     20000,
			minLayerSize:  32000,
			ztocGenerated: false,
		},
		{
			name:          "build ztoc: layer size 500 bytes, minimal layer size 500 bytes",
			layerSize:     500,
			minLayerSize:  500,
			ztocGenerated: true,
		},
		{
			name:          "build ztoc: layer size 20kB, minimal layer size 500 bytes",
			layerSize:     20000,
			minLayerSize:  500,
			ztocGenerated: true,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cs := newFakeContentStore()
			desc := ocispec.Descriptor{
				MediaType: "application/vnd.oci.image.layer.",
				Size:      tc.layerSize,
			}
			cfg := &buildConfig{
				minLayerSize: tc.minLayerSize,
			}
			spanSize := int64(65535)
			blobStore := memory.New()
			ztoc, err := buildSociLayer(ctx, cs, desc, spanSize, blobStore, cfg)
			if tc.ztocGenerated {
				// we check only for build skip, which is indicated as nil value for ztoc and nil value for error
				if ztoc == nil && err == nil {
					t.Fatalf("%v: ztoc should've been generated; error=%v", tc.name, err)
				}
			} else {
				if ztoc != nil {
					t.Fatalf("%v: ztoc should've skipped", tc.name)
				}
			}
		})
	}
}
