# {{classname}}

All URIs are relative to *http://localhost:8080/*

Method | HTTP request | Description
------------- | ------------- | -------------
[**FunctionsGet**](DefaultApi.md#FunctionsGet) | **Get** /functions | 
[**FunctionsPost**](DefaultApi.md#FunctionsPost) | **Post** /functions | 
[**InvocationsPost**](DefaultApi.md#InvocationsPost) | **Post** /invocations | 
[**MetricsGet**](DefaultApi.md#MetricsGet) | **Get** /metrics | 
[**NetIfacesNamespacePut**](DefaultApi.md#NetIfacesNamespacePut) | **Put** /net-ifaces/{namespace} | 
[**SnapshotsPost**](DefaultApi.md#SnapshotsPost) | **Post** /snapshots | 
[**SnapshotsPut**](DefaultApi.md#SnapshotsPut) | **Put** /snapshots | 
[**SnapshotsSsIdMincoreGet**](DefaultApi.md#SnapshotsSsIdMincoreGet) | **Get** /snapshots/{ssId}/mincore | 
[**SnapshotsSsIdMincorePatch**](DefaultApi.md#SnapshotsSsIdMincorePatch) | **Patch** /snapshots/{ssId}/mincore | 
[**SnapshotsSsIdMincorePost**](DefaultApi.md#SnapshotsSsIdMincorePost) | **Post** /snapshots/{ssId}/mincore | 
[**SnapshotsSsIdMincorePut**](DefaultApi.md#SnapshotsSsIdMincorePut) | **Put** /snapshots/{ssId}/mincore | 
[**SnapshotsSsIdPatch**](DefaultApi.md#SnapshotsSsIdPatch) | **Patch** /snapshots/{ssId} | 
[**SnapshotsSsIdPost**](DefaultApi.md#SnapshotsSsIdPost) | **Post** /snapshots/{ssId} | 
[**SnapshotsSsIdReapDelete**](DefaultApi.md#SnapshotsSsIdReapDelete) | **Delete** /snapshots/{ssId}/reap | 
[**SnapshotsSsIdReapGet**](DefaultApi.md#SnapshotsSsIdReapGet) | **Get** /snapshots/{ssId}/reap | 
[**SnapshotsSsIdReapPatch**](DefaultApi.md#SnapshotsSsIdReapPatch) | **Patch** /snapshots/{ssId}/reap | 
[**UiDataGet**](DefaultApi.md#UiDataGet) | **Get** /ui/data | 
[**UiGet**](DefaultApi.md#UiGet) | **Get** /ui | 
[**VmmsPost**](DefaultApi.md#VmmsPost) | **Post** /vmms | 
[**VmsGet**](DefaultApi.md#VmsGet) | **Get** /vms | 
[**VmsPost**](DefaultApi.md#VmsPost) | **Post** /vms | 
[**VmsVmIdDelete**](DefaultApi.md#VmsVmIdDelete) | **Delete** /vms/{vmId} | 
[**VmsVmIdGet**](DefaultApi.md#VmsVmIdGet) | **Get** /vms/{vmId} | 

# **FunctionsGet**
> []Function FunctionsGet(ctx, )


Return a list of functions

### Required Parameters
This endpoint does not need any parameter.

### Return type

[**[]Function**](Function.md)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **FunctionsPost**
> FunctionsPost(ctx, optional)


Create a new function

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
 **optional** | ***DefaultApiFunctionsPostOpts** | optional parameters | nil if no parameters

### Optional Parameters
Optional parameters are passed through a pointer to a DefaultApiFunctionsPostOpts struct
Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **body** | [**optional.Interface of Function**](Function.md)|  | 

### Return type

 (empty response body)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: */*
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **InvocationsPost**
> InlineResponse2001 InvocationsPost(ctx, optional)


Post an invocation

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
 **optional** | ***DefaultApiInvocationsPostOpts** | optional parameters | nil if no parameters

### Optional Parameters
Optional parameters are passed through a pointer to a DefaultApiInvocationsPostOpts struct
Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **body** | [**optional.Interface of Invocation**](Invocation.md)|  | 

### Return type

[**InlineResponse2001**](inline_response_200_1.md)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: */*
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **MetricsGet**
> MetricsGet(ctx, )


Metrics

### Required Parameters
This endpoint does not need any parameter.

### Return type

 (empty response body)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: Not defined

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **NetIfacesNamespacePut**
> NetIfacesNamespacePut(ctx, namespace, optional)


Put a vm network

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
  **namespace** | **string**|  | 
 **optional** | ***DefaultApiNetIfacesNamespacePutOpts** | optional parameters | nil if no parameters

### Optional Parameters
Optional parameters are passed through a pointer to a DefaultApiNetIfacesNamespacePutOpts struct
Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------

 **body** | [**optional.Interface of NetifacesNamespaceBody**](NetifacesNamespaceBody.md)|  | 

### Return type

 (empty response body)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: */*
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **SnapshotsPost**
> Snapshot SnapshotsPost(ctx, optional)


Take a snapshot

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
 **optional** | ***DefaultApiSnapshotsPostOpts** | optional parameters | nil if no parameters

### Optional Parameters
Optional parameters are passed through a pointer to a DefaultApiSnapshotsPostOpts struct
Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **body** | [**optional.Interface of Snapshot**](Snapshot.md)|  | 

### Return type

[**Snapshot**](Snapshot.md)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: */*
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **SnapshotsPut**
> Snapshot SnapshotsPut(ctx, fromSnapshot, memFilePath)


Put snapshot (copy)

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
  **fromSnapshot** | **string**|  | 
  **memFilePath** | **string**|  | 

### Return type

[**Snapshot**](Snapshot.md)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **SnapshotsSsIdMincoreGet**
> InlineResponse200 SnapshotsSsIdMincoreGet(ctx, ssId)


Get mincore state

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
  **ssId** | **string**|  | 

### Return type

[**InlineResponse200**](inline_response_200.md)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **SnapshotsSsIdMincorePatch**
> SnapshotsSsIdMincorePatch(ctx, body, ssId)


Change mincore state

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
  **body** | [**SsIdMincoreBody1**](SsIdMincoreBody1.md)|  | 
  **ssId** | **string**|  | 

### Return type

 (empty response body)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: */*
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **SnapshotsSsIdMincorePost**
> SnapshotsSsIdMincorePost(ctx, body, ssId)


Add mincore layer

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
  **body** | [**SsIdMincoreBody**](SsIdMincoreBody.md)|  | 
  **ssId** | **string**|  | 

### Return type

 (empty response body)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: */*
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **SnapshotsSsIdMincorePut**
> SnapshotsSsIdMincorePut(ctx, ssId, optional)


Put mincore state

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
  **ssId** | **string**|  | 
 **optional** | ***DefaultApiSnapshotsSsIdMincorePutOpts** | optional parameters | nil if no parameters

### Optional Parameters
Optional parameters are passed through a pointer to a DefaultApiSnapshotsSsIdMincorePutOpts struct
Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------

 **source** | **optional.String**|  | 

### Return type

 (empty response body)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **SnapshotsSsIdPatch**
> SnapshotsSsIdPatch(ctx, ssId, optional)


Change snapshot state

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
  **ssId** | **string**|  | 
 **optional** | ***DefaultApiSnapshotsSsIdPatchOpts** | optional parameters | nil if no parameters

### Optional Parameters
Optional parameters are passed through a pointer to a DefaultApiSnapshotsSsIdPatchOpts struct
Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------

 **body** | [**optional.Interface of SnapshotsSsIdBody**](SnapshotsSsIdBody.md)|  | 

### Return type

 (empty response body)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: */*
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **SnapshotsSsIdPost**
> Vm SnapshotsSsIdPost(ctx, ssId, optional)


Load a snapshot

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
  **ssId** | **string**|  | 
 **optional** | ***DefaultApiSnapshotsSsIdPostOpts** | optional parameters | nil if no parameters

### Optional Parameters
Optional parameters are passed through a pointer to a DefaultApiSnapshotsSsIdPostOpts struct
Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------

 **body** | [**optional.Interface of Invocation**](Invocation.md)|  | 

### Return type

[**Vm**](VM.md)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: */*
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **SnapshotsSsIdReapDelete**
> SnapshotsSsIdReapDelete(ctx, ssId)


delete reap state

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
  **ssId** | **string**|  | 

### Return type

 (empty response body)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **SnapshotsSsIdReapGet**
> SnapshotsSsIdReapGet(ctx, ssId)


get reap state

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
  **ssId** | **string**|  | 

### Return type

 (empty response body)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **SnapshotsSsIdReapPatch**
> SnapshotsSsIdReapPatch(ctx, ssId, optional)


Change reap state

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
  **ssId** | **string**|  | 
 **optional** | ***DefaultApiSnapshotsSsIdReapPatchOpts** | optional parameters | nil if no parameters

### Optional Parameters
Optional parameters are passed through a pointer to a DefaultApiSnapshotsSsIdReapPatchOpts struct
Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------

 **body** | [**optional.Interface of bool**](bool.md)|  | 

### Return type

 (empty response body)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: */*
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **UiDataGet**
> UiDataGet(ctx, )


UI

### Required Parameters
This endpoint does not need any parameter.

### Return type

 (empty response body)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: Not defined

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **UiGet**
> UiGet(ctx, )


UI

### Required Parameters
This endpoint does not need any parameter.

### Return type

 (empty response body)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: Not defined

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **VmmsPost**
> Vm VmmsPost(ctx, optional)


Create a VMM in the pool

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
 **optional** | ***DefaultApiVmmsPostOpts** | optional parameters | nil if no parameters

### Optional Parameters
Optional parameters are passed through a pointer to a DefaultApiVmmsPostOpts struct
Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **body** | [**optional.Interface of VmmsBody**](VmmsBody.md)|  | 

### Return type

[**Vm**](VM.md)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: */*
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **VmsGet**
> []Vm VmsGet(ctx, )


Returns a list of active VMs

### Required Parameters
This endpoint does not need any parameter.

### Return type

[**[]Vm**](VM.md)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **VmsPost**
> Vm VmsPost(ctx, optional)


Create a new VM

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
 **optional** | ***DefaultApiVmsPostOpts** | optional parameters | nil if no parameters

### Optional Parameters
Optional parameters are passed through a pointer to a DefaultApiVmsPostOpts struct
Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **body** | [**optional.Interface of VmsBody**](VmsBody.md)|  | 

### Return type

[**Vm**](VM.md)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: */*
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **VmsVmIdDelete**
> VmsVmIdDelete(ctx, vmId)


Stop a VM

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
  **vmId** | **string**|  | 

### Return type

 (empty response body)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **VmsVmIdGet**
> Vm VmsVmIdGet(ctx, vmId)


Describe a VM

### Required Parameters

Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
  **vmId** | **string**|  | 

### Return type

[**Vm**](VM.md)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: */*

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

