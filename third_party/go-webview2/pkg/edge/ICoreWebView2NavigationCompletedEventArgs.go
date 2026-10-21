package edge

import "unsafe"

type _ICoreWebView2NavigationCompletedEventArgsVtbl struct {
	_IUnknownVtbl
	GetIsSuccess      ComProc
	GetWebErrorStatus ComProc
	GetNavigationId   ComProc
}

type ICoreWebView2NavigationCompletedEventArgs struct {
	vtbl *_ICoreWebView2NavigationCompletedEventArgsVtbl
}

func (i *ICoreWebView2NavigationCompletedEventArgs) AddRef() uintptr {
	r, _, _ := i.vtbl.AddRef.Call()
	return r
}

// IsSuccess 页面是否加载成功。宿主靠它判断「白屏」是导航失败还是渲染问题
// （本项目实测：窗口与标题都正常但内容全白，只有这个值能分清）。
func (i *ICoreWebView2NavigationCompletedEventArgs) IsSuccess() bool {
	var v int32
	_, _, _ = i.vtbl.GetIsSuccess.Call(
		uintptr(unsafe.Pointer(i)),
		uintptr(unsafe.Pointer(&v)),
	)
	return v != 0
}

// WebErrorStatus 导航失败时的 COREWEBVIEW2_WEB_ERROR_STATUS（0 = 无错误）。
func (i *ICoreWebView2NavigationCompletedEventArgs) WebErrorStatus() int32 {
	var v int32
	_, _, _ = i.vtbl.GetWebErrorStatus.Call(
		uintptr(unsafe.Pointer(i)),
		uintptr(unsafe.Pointer(&v)),
	)
	return v
}
