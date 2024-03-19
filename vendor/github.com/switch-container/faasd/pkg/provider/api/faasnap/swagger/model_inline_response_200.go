/*
 * faasnap
 *
 * FaaSnap API
 *
 * API version: 1.0.0
 * Generated by: Swagger Codegen (https://github.com/swagger-api/swagger-codegen.git)
 */
package swagger

type InlineResponse200 struct {
	Nlayers int32 `json:"nlayers,omitempty"`
	NNzRegions int32 `json:"n_nz_regions,omitempty"`
	NzRegionSize int32 `json:"nz_region_size,omitempty"`
	NWsRegions int32 `json:"n_ws_regions,omitempty"`
	WsRegionSize int32 `json:"ws_region_size,omitempty"`
}
