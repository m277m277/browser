package browser

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	uurl "net/url"
	"time"

	"gitee.com/baixudong/bs4"
	"gitee.com/baixudong/cdp"
	"gitee.com/baixudong/cmd"
	"gitee.com/baixudong/db"
	"gitee.com/baixudong/re"
	"gitee.com/baixudong/requests"
	"gitee.com/baixudong/tools"
	"github.com/tidwall/gjson"
)

type Page struct {
	host         string
	port         int
	id           string
	mouseX       float64
	mouseY       float64
	ctx          context.Context
	cnl          context.CancelFunc
	preWebSock   *cdp.WebSock
	globalReqCli *requests.Client
	headless     bool

	baseUrl          string
	webSock          *cdp.WebSock
	stealth          bool
	isReplaceRequest bool
	pageAfterTime    *time.Timer
	domAfterTime     *time.Timer

	pageLoadId int64
	pageStop   bool
	domLoad    bool
	iframes    map[string]string
}

func defaultRequestFunc(ctx context.Context, r *cdp.Route) { r.RequestContinue(ctx) }
func (obj *Page) pageStartLoadMain(ctx context.Context, rd cdp.RecvData) {
	if obj.id == rd.Params["frameId"].(string) {
		if rd.Id > obj.pageLoadId {
			obj.pageLoadId = rd.Id
			obj.domLoad = false
			obj.pageStop = false
			obj.iframes = make(map[string]string)
		}
	}
}
func (obj *Page) pageEndLoadMain(ctx context.Context, rd cdp.RecvData) {
	if obj.id == rd.Params["frameId"].(string) {
		if rd.Id > obj.pageLoadId {
			obj.pageLoadId = rd.Id
			obj.domLoad = true
			obj.pageStop = true
		}
	}
}

func (obj *Page) domLoadMain(ctx context.Context, rd cdp.RecvData) {
	obj.domLoad = true
}
func (obj *Page) frameLoadMain(ctx context.Context, rd cdp.RecvData) {
	jsonData, err := tools.Any2json(rd.Params)
	if err != nil {
		return
	}
	href := jsonData.Get("frame.url").String()
	frameId := jsonData.Get("frame.id").String()
	if href != "" && frameId != "" {
		obj.iframes[href] = frameId
	}
}

func (obj *Page) addEvent(method string, fun func(ctx context.Context, rd cdp.RecvData)) {
	obj.webSock.AddEvent(method, fun)
}

//go:embed getInjectableScript.js
var getInjectableScript string

//go:embed stealthNew.js
var stealth string

func (obj *Page) init(globalReqCli *requests.Client, option PageOption, db *db.Client) error {
	var err error
	if obj.webSock, err = cdp.NewWebSock(
		obj.ctx,
		globalReqCli,
		fmt.Sprintf("ws://%s:%d/devtools/page/%s", obj.host, obj.port, obj.id),
		cdp.WebSockOption{
			IsReplaceRequest: obj.isReplaceRequest,
			Proxy:            option.Proxy,
		},
		db,
	); err != nil {
		return err
	}
	obj.addEvent("Page.frameStartedLoading", obj.pageStartLoadMain)
	obj.addEvent("Page.frameStoppedLoading", obj.pageEndLoadMain)
	obj.addEvent("Page.domContentEventFired", obj.domLoadMain)
	obj.addEvent("Page.frameNavigated", obj.frameLoadMain)
	if _, err = obj.webSock.PageEnable(obj.ctx); err != nil {
		return err
	}
	if option.Stealth || obj.stealth {
		if err = obj.AddScript(obj.ctx, stealth); err != nil {
			return err
		}
	}
	return obj.AddScript(obj.ctx, `Object.defineProperty(window, "RTCPeerConnection",{"get":undefined});Object.defineProperty(window, "mozRTCPeerConnection",{"get":undefined});Object.defineProperty(window, "webkitRTCPeerConnection",{"get":undefined});`)
}

type FpOption struct {
	Browser         string //("chrome" | "firefox" | "safari" | "edge")
	Device          string //"mobile" | "desktop"
	OperatingSystem string //"windows" | "macos" | "linux" | "android" | "ios"
	UserAgent       string
	Locales         []string
	Locale          string
}

func CreateFp(options ...FpOption) (string, error) {
	screen := map[string]any{
		"availHeight":      672,
		"availWidth":       1280,
		"pixelDepth":       24,
		"height":           720,
		"width":            1280,
		"availTop":         0,
		"availLeft":        0,
		"colorDepth":       24,
		"innerHeight":      0,
		"outerHeight":      672,
		"outerWidth":       1280,
		"innerWidth":       0,
		"screenX":          0,
		"pageXOffset":      0,
		"pageYOffset":      0,
		"devicePixelRatio": 1.5,
		"clientWidth":      0,
		"clientHeight":     18,
		"hasHDR":           false,
	}
	audioCodecs := map[string]any{
		"ogg": "probably",
		"mp3": "probably",
		"wav": "probably",
		"m4a": "maybe",
		"aac": "probably",
	}
	videoCodecs := map[string]any{
		"ogg":  "probably",
		"h264": "probably",
		"webm": "probably",
	}
	battery := map[string]any{
		"charging":        true,
		"chargingTime":    0,
		"dischargingTime": nil,
		"level":           1,
	}
	videoCard := map[string]any{
		"vendor":   "Google Inc. (Intel)",
		"renderer": "ANGLE (Intel, Intel(R) UHD Graphics Direct3D11 vs_5_0 ps_5_0, D3D11)",
	}
	multimediaDevices := map[string]any{
		"speakers": []map[string]any{
			{
				"deviceId": "",
				"kind":     "audiooutput",
				"label":    "",
				"groupId":  "",
			},
		},
		"micros": []map[string]any{
			{
				"deviceId": "",
				"kind":     "audioinput",
				"label":    "",
				"groupId":  "",
			},
		},
		"webcams": []map[string]any{
			{
				"deviceId": "",
				"kind":     "videoinput",
				"label":    "",
				"groupId":  "",
			},
		},
	}
	appVersion := re.Sub("Mozilla/", "", requests.UserAgent)
	version := re.Search(`Chrome/(\d+)?\.`, requests.UserAgent).Group(1)
	navigator := map[string]any{
		"userAgent": requests.UserAgent,
		"userAgentData": map[string]any{
			"brands": []map[string]any{
				{
					"brand":   "Microsoft Edge",
					"version": version,
				},
				{
					"brand":   "Not;A=Brand",
					"version": "8",
				},
				{
					"brand":   "Chromium",
					"version": version,
				},
			},
			"mobile":   false,
			"platform": "Windows",
		},
		"language": "zh-CN",
		"languages": []string{
			"zh-CN",
			"en",
			"en-GB",
			"en-US",
		},
		"platform":            "Win32",
		"deviceMemory":        8,
		"hardwareConcurrency": 8,
		"maxTouchPoints":      10,
		"product":             "Gecko",
		"productSub":          "20030107",
		"vendor":              "Google Inc.",
		"vendorSub":           "",
		"doNotTrack":          nil,
		"appCodeName":         "Mozilla",
		"appName":             "Netscape",
		"appVersion":          appVersion,
		"webdriver":           false,
	}
	fp := map[string]any{
		"screen":            screen,
		"audioCodecs":       audioCodecs,
		"videoCodecs":       videoCodecs,
		"battery":           battery,
		"videoCard":         videoCard,
		"multimediaDevices": multimediaDevices,
		"navigator":         navigator,
		"userAgent":         requests.UserAgent,
		"historyLength":     5,
	}
	cli, err := cmd.NewJsClient(nil, cmd.JsClientOption{
		Script: getInjectableScript,
		Names:  []string{"createFp"},
	})
	if err != nil {
		return "", err
	}
	result, err := cli.Call("createFp", fp)
	if err != nil {
		return "", err
	}
	return result.Get("result").String(), nil
}
func (obj *Page) AddScript(ctx context.Context, script string) error {
	_, err := obj.webSock.PageAddScriptToEvaluateOnNewDocument(ctx, script)
	return err
}
func (obj *Page) Screenshot(ctx context.Context, rect cdp.Rect, options ...cdp.ScreenshotOption) ([]byte, error) {
	jsonData, err := obj.Eval(ctx, `()=>{return document.documentElement.scrollTop}`, nil)
	if err != nil {
		return nil, err
	}
	rect.Y += jsonData.Get("result.value").Float()
	rs, err := obj.webSock.PageCaptureScreenshot(ctx, rect, options...)
	if err != nil {
		return nil, err
	}
	imgData, ok := rs.Result["data"].(string)
	if !ok {
		return nil, errors.New("not img data")
	}
	return tools.Base64Decode(imgData)
}

func (obj *Page) Rect(ctx context.Context) (cdp.Rect, error) {
	rs, err := obj.webSock.PageGetLayoutMetrics(ctx)
	var result cdp.Rect
	if err != nil {
		return result, err
	}
	return result, tools.Any2struct(rs.Result["cssContentSize"], &result)
}
func (obj *Page) Reload(ctx context.Context) error {
	_, err := obj.webSock.PageReload(ctx)
	return err
}
func (obj *Page) WaitPageStop(preCtx context.Context, waits ...time.Duration) error {
	var wait time.Duration
	if len(waits) > 0 {
		wait = waits[0]
	} else {
		wait = time.Second * 2
	}
	var ctx context.Context
	var cnl context.CancelFunc
	if preCtx == nil {
		ctx, cnl = context.WithTimeout(obj.ctx, time.Second*60)
	} else {
		ctx, cnl = context.WithTimeout(preCtx, time.Second*60)
	}
	defer cnl()
	for {
		if obj.pageAfterTime == nil {
			obj.pageAfterTime = time.NewTimer(wait)
		} else {
			obj.pageAfterTime.Reset(wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-obj.ctx.Done():
			return obj.ctx.Err()
		case <-obj.pageAfterTime.C:
			if obj.pageStop {
				return nil
			}
		}
	}
}
func (obj *Page) WaitDomLoad(preCtx context.Context, waits ...time.Duration) error {
	var wait time.Duration
	if len(waits) > 0 {
		wait = waits[0]
	} else {
		wait = time.Second * 2
	}
	var ctx context.Context
	var cnl context.CancelFunc
	if preCtx == nil {
		ctx, cnl = context.WithTimeout(obj.ctx, time.Second*60)
	} else {
		ctx, cnl = context.WithTimeout(preCtx, time.Second*60)
	}
	defer cnl()
	for {
		if obj.domAfterTime == nil {
			obj.domAfterTime = time.NewTimer(wait)
		} else {
			obj.domAfterTime.Reset(wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-obj.ctx.Done():
			return obj.ctx.Err()
		case <-obj.domAfterTime.C:
			if obj.domLoad {
				return nil
			}
		}
	}
}
func (obj *Page) GoTo(preCtx context.Context, url string) error {
	obj.baseUrl = url
	_, err := obj.webSock.PageNavigate(preCtx, url)
	return err
}

// ex:   ()=>{}  或者  (params)=>{}
func (obj *Page) Eval(ctx context.Context, expression string, params map[string]any) (gjson.Result, error) {
	var value string
	if params != nil {
		con, err := json.Marshal(params)
		if err != nil {
			return gjson.Result{}, err
		}
		value = tools.BytesToString(con)
	}
	// log.Print(fmt.Sprintf(`(async %s)(%s)`, expression, value))
	rs, err := obj.webSock.RuntimeEvaluate(ctx, fmt.Sprintf(`(async %s)(%s)`, expression, value))
	if err != nil {
		return gjson.Result{}, err
	}
	return tools.Any2json(rs.Result)
}
func (obj *Page) Close() error {
	defer func() {
		if obj.pageAfterTime != nil {
			obj.pageAfterTime.Stop()
		}
		if obj.domAfterTime != nil {
			obj.domAfterTime.Stop()
		}
	}()
	defer obj.cnl()
	_, err := obj.preWebSock.TargetCloseTarget(obj.id)
	if err != nil {
		err = obj.close()
	}
	obj.webSock.Close(nil)
	return err
}
func (obj *Page) close() error {
	resp, err := obj.globalReqCli.Request(context.TODO(), "get", fmt.Sprintf("http://%s:%d/json/close/%s", obj.host, obj.port, obj.id), requests.RequestOption{DisProxy: true})
	if err != nil {
		return err
	}
	if resp.Text() == "Target is closing" {
		return nil
	}
	return errors.New(resp.Text())
}

func (obj *Page) Done() <-chan struct{} {
	return obj.webSock.Done()
}
func (obj *Page) Request(ctx context.Context, RequestFunc func(context.Context, *cdp.Route)) error {
	if RequestFunc != nil {
		obj.webSock.RequestFunc = RequestFunc
	} else if obj.isReplaceRequest {
		obj.webSock.RequestFunc = defaultRequestFunc
	} else {
		obj.webSock.RequestFunc = nil
	}
	var err error
	if obj.webSock.RequestFunc != nil {
		_, err = obj.webSock.FetchRequestEnable(ctx)
	} else if obj.webSock.ResponseFunc == nil {
		_, err = obj.webSock.FetchDisable(ctx)
	}
	return err
}
func (obj *Page) Response(ctx context.Context, ResponseFunc func(context.Context, *cdp.Route)) error {
	obj.webSock.ResponseFunc = ResponseFunc
	var err error
	if obj.webSock.ResponseFunc != nil {
		_, err = obj.webSock.FetchResponseEnable(ctx)
	} else if obj.webSock.RequestFunc == nil {
		_, err = obj.webSock.FetchDisable(ctx)
	}
	return err
}
func (obj *Page) nodeId(ctx context.Context) (int64, error) {
	rs, err := obj.webSock.DOMGetDocument(ctx)
	if err != nil {
		return 0, err
	}
	jsonData, err := tools.Any2json(rs.Result["root"])
	if err != nil {
		return 0, err
	}
	href := jsonData.Get("baseURL").String()
	if href != "" {
		obj.baseUrl = href
	}
	return jsonData.Get("nodeId").Int(), nil
}
func (obj *Page) Html(ctx context.Context, contents ...string) (*bs4.Client, error) {
	nodeId, err := obj.nodeId(ctx)
	if err != nil {
		return nil, err
	}
	return obj.HtmlWithNodeId(ctx, nodeId, contents...)
}
func (obj *Page) HtmlWithNodeId(ctx context.Context, nodeId int64, contents ...string) (*bs4.Client, error) {
	if len(contents) > 0 {
		return nil, obj.setHtml(ctx, nodeId, contents[0])
	}
	return obj.html(ctx, nodeId)
}
func (obj *Page) setHtml(ctx context.Context, nodeId int64, content string) error {
	_, err := obj.webSock.DOMSetOuterHTML(ctx, nodeId, content)
	return err
}
func (obj *Page) html(ctx context.Context, nodeId int64) (*bs4.Client, error) {
	rs, err := obj.webSock.DOMGetOuterHTML(ctx, nodeId, 0)
	if err != nil {
		return nil, err
	}
	html := bs4.NewClient(rs.Result["outerHTML"].(string), obj.baseUrl)
	for _, iframe := range html.Finds("iframe") {
		href := iframe.Get("src")
		iframeId, ok := obj.iframes[href]
		if ok {
			frameHtml, err := obj.getFrameHtml(ctx, iframeId)
			if err != nil {
				return nil, err
			}
			iframe.SetHtml(frameHtml)
		}
	}
	return html, nil
}
func (obj *Page) WaitSelector(ctx context.Context, selector string, timeouts ...time.Duration) (*Dom, error) {
	nodeId, err := obj.nodeId(ctx)
	if err != nil {
		return nil, err
	}
	return obj.WaitSelectorWithNodeId(ctx, nodeId, selector, false, timeouts...)
}
func (obj *Page) WaitSelectorWithNodeId(preCtx context.Context, nodeId int64, selector string, isPage bool, timeouts ...time.Duration) (*Dom, error) {
	if preCtx == nil {
		preCtx = obj.ctx
	}
	var timeout time.Duration
	if len(timeouts) > 0 {
		timeout = timeouts[0]
	} else {
		timeout = time.Second * 30
	}
	startTime := time.Now()
	var t *time.Timer
	defer func() {
		if t != nil {
			t.Stop()
		}
	}()
	for time.Since(startTime) <= timeout {
		dom, err := obj.QuerySelectorWithNodeId(preCtx, nodeId, selector, isPage)
		if err != nil {
			return nil, err
		}
		if dom != nil {
			return dom, nil
		}
		if t == nil {
			t = time.NewTimer(time.Millisecond * 500)
		} else {
			t.Reset(time.Millisecond * 500)
		}
		select {
		case <-t.C:
		case <-preCtx.Done():
			return nil, preCtx.Err()
		}
	}
	return nil, errors.New("超时")
}
func (obj *Page) QuerySelector(ctx context.Context, selector string) (*Dom, error) {
	return obj.QuerySelectorWithNodeId(ctx, 0, selector, true)
}
func (obj *Page) QuerySelectorWithNodeId(ctx context.Context, nodeId int64, selector string, isPage bool) (dom *Dom, err error) {
	dom, err = obj.querySelector(ctx, nodeId, selector, isPage)
	if err != nil {
		return dom, err
	}
	if dom == nil && selector != "iframe" {
		iframes, err := obj.querySelectorAll(ctx, nodeId, "iframe", isPage)
		if err != nil {
			return nil, err
		}
		for _, iframe := range iframes {
			dom, err = obj.querySelector(ctx, iframe.nodeId, selector, isPage)
			if err != nil || dom != nil {
				return dom, err
			}
		}
	}
	return dom, err
}
func (obj *Page) querySelector(ctx context.Context, nodeId int64, selector string, isPage bool) (dom *Dom, err error) {
	if isPage {
		if nodeId, err = obj.nodeId(ctx); err != nil {
			return nil, err
		}
	}
	rs, err := obj.webSock.DOMQuerySelector(ctx, nodeId, selector)
	if err != nil {
		return nil, err
	}
	if rs.Result == nil {
		return nil, nil
	}
	nodeIdAny, ok := rs.Result["nodeId"]
	if !ok {
		return nil, errors.New("not found")
	}
	nodeId = int64(nodeIdAny.(float64))
	if nodeId == 0 {
		return nil, nil
	}
	dom = &Dom{
		baseUrl: obj.baseUrl,
		webSock: obj.webSock,
		nodeId:  nodeId,
	}
	if re.Search(`^iframe\W|\Wiframe\W|\Wiframe$|^iframe$`, selector) != nil {
		if err = dom.frame2Dom(ctx); err != nil {
			return nil, err
		}
	}
	return dom, nil
}
func (obj *Page) QuerySelectorAll(ctx context.Context, selector string) ([]*Dom, error) {
	nodeId, err := obj.nodeId(ctx)
	if err != nil {
		return nil, err
	}
	return obj.QuerySelectorAllWithNodeId(ctx, nodeId, selector, true)
}
func (obj *Page) QuerySelectorAllWithNodeId(ctx context.Context, nodeId int64, selector string, isPage bool) ([]*Dom, error) {
	dom, err := obj.querySelectorAll(ctx, nodeId, selector, isPage)
	if err != nil {
		return dom, err
	}
	if dom == nil && selector != "iframe" {
		iframes, err := obj.querySelectorAll(ctx, nodeId, "iframe", isPage)
		if err != nil {
			return nil, err
		}
		doms := []*Dom{}
		for _, iframe := range iframes {
			dom, err = obj.querySelectorAll(ctx, iframe.nodeId, selector, isPage)
			if err != nil {
				return dom, err
			}
			doms = append(doms, dom...)
		}
		return doms, err
	}
	return dom, err
}
func (obj *Page) querySelectorAll(ctx context.Context, nodeId int64, selector string, isPage bool) (doms []*Dom, err error) {
	if isPage {
		if nodeId, err = obj.nodeId(ctx); err != nil {
			return nil, err
		}
	}
	rs, err := obj.webSock.DOMQuerySelectorAll(ctx, nodeId, selector)
	if err != nil {
		return nil, err
	}
	if rs.Result["nodeIds"] == nil {
		return nil, nil
	}
	jsonData, err := tools.Any2json(rs.Result["nodeIds"])
	if err != nil {
		return nil, err
	}
	doms = []*Dom{}
	for _, nodeId := range jsonData.Array() {
		dom := &Dom{
			baseUrl: obj.baseUrl,
			webSock: obj.webSock,
			nodeId:  nodeId.Int(),
		}
		if re.Search(`^iframe\W|\Wiframe\W|\Wiframe$|^iframe$`, selector) != nil {
			if err = dom.frame2Dom(ctx); err != nil {
				return nil, err
			}
		}
		doms = append(doms, dom)
	}
	return doms, nil
}
func (obj *Page) Focus(ctx context.Context, nodeId int64) error {
	_, err := obj.webSock.DOMFocus(ctx, nodeId)
	return err
}
func (obj *Page) sendChar(ctx context.Context, chr rune) error {
	_, err := obj.webSock.InputDispatchKeyEvent(ctx, cdp.DispatchKeyEventOption{
		Type: "keyDown",
		Key:  "Unidentified",
	})
	if err != nil {
		return err
	}
	_, err = obj.webSock.InputDispatchKeyEvent(ctx, cdp.DispatchKeyEventOption{
		Type:           "keyDown",
		Key:            "Unidentified",
		Text:           string(chr),
		UnmodifiedText: string(chr),
	})
	if err != nil {
		return err
	}
	_, err = obj.webSock.InputDispatchKeyEvent(ctx, cdp.DispatchKeyEventOption{
		Type: "keyUp",
		Key:  "Unidentified",
	})
	return err
}
func (obj *Page) SendText(ctx context.Context, nodeId int64, text string) error {
	err := obj.Focus(ctx, nodeId)
	if err != nil {
		return err
	}
	for _, chr := range text {
		err = obj.sendChar(ctx, chr)
		if err != nil {
			return err
		}
	}
	return nil
}

// 移动操作
func (obj *Page) baseMove(ctx context.Context, x, y float64, kind int, steps ...int) error {
	var step int
	if len(steps) > 0 {
		step = steps[0]
	}
	if step < 1 {
		step = 1
	}
	for _, poi := range tools.GetTrack(
		[2]float64{obj.mouseX, obj.mouseY},
		[2]float64{obj.mouseX + x, obj.mouseY + y},
		float64(step),
	) {
		switch kind {
		case 0:
			if err := obj.move(ctx, cdp.Point{
				X: poi[0],
				Y: poi[1],
			}); err != nil {
				return err
			}
		case 1:
			if err := obj.touchMove(ctx, cdp.Point{
				X: poi[0],
				Y: poi[1],
			}); err != nil {
				return err
			}
		default:
			return errors.New("not found kind")
		}
	}
	obj.mouseX = obj.mouseX + x
	obj.mouseY = obj.mouseY + y
	return nil
}

func (obj *Page) Move(ctx context.Context, x, y float64, steps ...int) error {
	return obj.baseMove(ctx, x, y, 0, steps...)
}

func (obj *Page) move(ctx context.Context, point cdp.Point) error {
	_, err := obj.webSock.InputDispatchMouseEvent(ctx,
		cdp.DispatchMouseEventOption{
			Type: "mouseMoved",
			X:    point.X,
			Y:    point.Y,
		})
	if err != nil {
		return err
	}
	obj.mouseX = point.X
	obj.mouseY = point.Y
	return nil
}
func (obj *Page) TouchMove(ctx context.Context, x, y float64, steps ...int) error {
	return obj.baseMove(ctx, x, y, 1, steps...)
}
func (obj *Page) touchMove(ctx context.Context, point cdp.Point) error { //不需要delta
	_, err := obj.webSock.InputDispatchTouchEvent(ctx, "touchMove", []cdp.Point{
		{
			X: point.X,
			Y: point.Y,
		},
	})
	if err != nil {
		return err
	}
	obj.mouseX = point.X
	obj.mouseY = point.Y
	return nil
}
func (obj *Page) Wheel(ctx context.Context, x, y float64) error {
	_, err := obj.webSock.InputDispatchMouseEvent(ctx,
		cdp.DispatchMouseEventOption{
			Type:   "mouseWheel",
			DeltaX: x,
			DeltaY: y,
		})
	return err
}
func (obj *Page) Down(ctx context.Context, point cdp.Point) error {
	_, err := obj.webSock.InputDispatchMouseEvent(ctx,
		cdp.DispatchMouseEventOption{
			Type:       "mousePressed",
			Button:     "left",
			X:          point.X,
			Y:          point.Y,
			ClickCount: 1,
		})
	if err != nil {
		return err
	}
	obj.mouseX = point.X
	obj.mouseY = point.Y
	return err
}
func (obj *Page) TouchDown(ctx context.Context, point cdp.Point) error {
	_, err := obj.webSock.InputDispatchTouchEvent(ctx, "touchStart",
		[]cdp.Point{
			{
				X: point.X,
				Y: point.Y,
			},
		})
	if err != nil {
		return err
	}
	obj.mouseX = point.X
	obj.mouseY = point.Y
	return nil
}
func (obj *Page) Up(ctx context.Context) error {
	_, err := obj.webSock.InputDispatchMouseEvent(ctx, cdp.DispatchMouseEventOption{
		Type:       "mouseReleased",
		Button:     "left",
		X:          obj.mouseX,
		Y:          obj.mouseY,
		ClickCount: 1,
	})
	return err
}
func (obj *Page) TouchUp(ctx context.Context) error {
	_, err := obj.webSock.InputDispatchTouchEvent(ctx,
		"touchEnd",
		[]cdp.Point{})
	return err
}
func (obj *Page) Click(ctx context.Context, point cdp.Point) error {
	if err := obj.Down(ctx, point); err != nil {
		return err
	}
	return obj.Up(ctx)
}
func (obj *Page) TouchClick(ctx context.Context, point cdp.Point) error {
	if err := obj.TouchDown(ctx, point); err != nil {
		return err
	}
	return obj.TouchUp(ctx)
}

// 设置移动设备的属性
func (obj *Page) SetDevice(ctx context.Context, device cdp.Device) error {
	if err := obj.SetUserAgent(ctx, device.UserAgent); err != nil {
		return err
	}
	if err := obj.SetTouch(ctx, device.HasTouch); err != nil {
		return err
	}
	return obj.SetDeviceMetrics(ctx, device)
}

func (obj *Page) SetUserAgent(ctx context.Context, userAgent string) error {
	_, err := obj.webSock.EmulationSetUserAgentOverride(ctx, userAgent)
	return err
}

// 设置设备指标
func (obj *Page) SetDeviceMetrics(ctx context.Context, device cdp.Device) error {
	_, err := obj.webSock.EmulationSetDeviceMetricsOverride(ctx, device)
	return err
}

// 设置设备是否支持触摸
func (obj *Page) SetTouch(ctx context.Context, hasTouch bool) error {
	_, err := obj.webSock.EmulationSetTouchEmulationEnabled(ctx, hasTouch)
	return err
}

func (obj *Page) SetCookies(ctx context.Context, href string, cookies ...cdp.Cookie) error {
	if len(cookies) == 0 {
		return nil
	}
	if href == "" {
		href = obj.baseUrl
	}
	var err error
	for i := 0; i < len(cookies); i++ {
		if cookies[i].Domain == "" {
			if cookies[i].Url == "" {
				cookies[i].Url = href
			}
			if cookies[i].Url != "" {
				us, err := uurl.Parse(cookies[i].Url)
				if err != nil {
					return err
				}
				cookies[i].Domain = us.Hostname()
			}
		}
	}
	_, err = obj.webSock.NetworkSetCookies(ctx, cookies)
	return err
}
func (obj *Page) GetCookies(ctx context.Context, urls ...string) (cdp.Cookies, error) {
	if len(urls) == 0 {
		urls = append(urls, obj.baseUrl)
	}
	rs, err := obj.webSock.NetworkGetCookies(ctx, urls...)
	if err != nil {
		return nil, err
	}
	jsonData, err := tools.Any2json(rs.Result)
	if err != nil {
		return nil, err
	}
	result := []cdp.Cookie{}
	for _, cookie := range jsonData.Get("cookies").Array() {
		var cook cdp.Cookie
		if err = json.Unmarshal(tools.StringToBytes(cookie.Raw), &cook); err != nil {
			return result, err
		}
		result = append(result, cook)
	}
	return result, nil
}

func (obj *Page) ClearCookies(ctx context.Context) (err error) {
	_, err = obj.webSock.NetworkClearBrowserCookies(ctx)
	return
}
func (obj *Page) ClearCache(ctx context.Context) (err error) {
	_, err = obj.webSock.NetworkClearBrowserCache(ctx)
	return
}
func (obj *Page) ClearStorage(ctx context.Context) (err error) {
	_, err = obj.webSock.StorageClear(ctx, obj.baseUrl)
	return
}
