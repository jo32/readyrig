//go:build darwin && cgo

package computer

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreGraphics -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>
#include <CoreGraphics/CoreGraphics.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
static int screenAllowed(){return CGPreflightScreenCaptureAccess();}
static int inputAllowed(){return AXIsProcessTrusted();}
// Requesting, not just checking, is what makes macOS list this app in
// Privacy & Security; a bare preflight never registers it.
static void requestScreen(){CGRequestScreenCaptureAccess();}
static void requestInput(){
  const void *k[]={kAXTrustedCheckOptionPrompt}; const void *v[]={kCFBooleanTrue};
  CFDictionaryRef o=CFDictionaryCreate(NULL,k,v,1,&kCFTypeDictionaryKeyCallBacks,&kCFTypeDictionaryValueCallBacks);
  AXIsProcessTrustedWithOptions(o); CFRelease(o);
}
static double displayW(){return CGDisplayBounds(CGMainDisplayID()).size.width;}
static double displayH(){return CGDisplayBounds(CGMainDisplayID()).size.height;}
static int corner(){CGEventRef e=CGEventCreate(NULL); if(!e)return 0; CGPoint p=CGEventGetLocation(e);CFRelease(e);return (p.x<=2 || p.x>=displayW()-2) && (p.y<=2 || p.y>=displayH()-2);}
static void mouseEvent(int type,double x,double y,int button,int count,unsigned long long flags){CGEventRef e=CGEventCreateMouseEvent(NULL,(CGEventType)type,CGPointMake(x,y),(CGMouseButton)button);CGEventSetIntegerValueField(e,kCGMouseEventClickState,count);CGEventSetFlags(e,(CGEventFlags)flags);CGEventPost(kCGHIDEventTap,e);CFRelease(e);}
static void scrollEvent(int x,int y,unsigned long long flags){CGEventRef e=CGEventCreateScrollWheelEvent(NULL,kCGScrollEventUnitPixel,2,y,x);CGEventSetFlags(e,(CGEventFlags)flags);CGEventPost(kCGHIDEventTap,e);CFRelease(e);}
static void keyEvent(int code,int down,unsigned long long flags){CGEventRef e=CGEventCreateKeyboardEvent(NULL,(CGKeyCode)code,down);CGEventSetFlags(e,(CGEventFlags)flags);CGEventPost(kCGHIDEventTap,e);CFRelease(e);}
static void unicodeEvent(UniChar *chars,int count){CGEventRef e=CGEventCreateKeyboardEvent(NULL,0,true);CGEventKeyboardSetUnicodeString(e,count,chars);CGEventPost(kCGHIDEventTap,e);CGEventSetType(e,kCGEventKeyUp);CGEventPost(kCGHIDEventTap,e);CFRelease(e);}

static int displayInfo(int idx,double *x,double *y,double *w,double *h){
  CGDirectDisplayID ids[16]; uint32_t n=0;
  if (CGGetActiveDisplayList(16,ids,&n)!=kCGErrorSuccess || idx<0 || idx>=(int)n) return 0;
  CGRect r=CGDisplayBounds(ids[idx]);
  *x=r.origin.x; *y=r.origin.y; *w=r.size.width; *h=r.size.height;
  return (int)n;
}

typedef struct { char *p; size_t len, cap; } sbuf;
static void sbAdd(sbuf *b,const char *s){
  size_t n=strlen(s);
  if (b->len+n+1>b->cap){ size_t c=b->cap?b->cap*2:4096; while(c<b->len+n+1) c*=2; b->p=realloc(b->p,c); b->cap=c; }
  memcpy(b->p+b->len,s,n); b->len+=n; b->p[b->len]=0;
}
static void cfUTF8(CFStringRef s,char *out,size_t n){
  out[0]=0; if(!s) return;
  if(!CFStringGetCString(s,out,n,kCFStringEncodingUTF8)){ out[0]=0; return; }
  for(char *q=out;*q;q++) if(*q=='\t'||*q=='\n'||*q=='\r') *q=' ';
}

// One line per window, front to back: pid, owner, title, x, y, w, h, layer, number.
static char *windowList(void){
  CFArrayRef arr=CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly|kCGWindowListExcludeDesktopElements,kCGNullWindowID);
  if(!arr) return NULL;
  sbuf b; memset(&b,0,sizeof b);
  CFIndex n=CFArrayGetCount(arr);
  for(CFIndex i=0;i<n;i++){
    CFDictionaryRef d=(CFDictionaryRef)CFArrayGetValueAtIndex(arr,i);
    int layer=0,pid=0; CFNumberRef ln=(CFNumberRef)CFDictionaryGetValue(d,kCGWindowLayer); if(ln) CFNumberGetValue(ln,kCFNumberIntType,&layer);
    CFNumberRef pn=(CFNumberRef)CFDictionaryGetValue(d,kCGWindowOwnerPID); if(pn) CFNumberGetValue(pn,kCFNumberIntType,&pid);
    int num=0; CFNumberRef wn=(CFNumberRef)CFDictionaryGetValue(d,kCGWindowNumber); if(wn) CFNumberGetValue(wn,kCFNumberIntType,&num);
    char owner[256],title[512];
    cfUTF8((CFStringRef)CFDictionaryGetValue(d,kCGWindowOwnerName),owner,sizeof owner);
    cfUTF8((CFStringRef)CFDictionaryGetValue(d,kCGWindowName),title,sizeof title);
    CGRect r=CGRectZero; CFDictionaryRef bd=(CFDictionaryRef)CFDictionaryGetValue(d,kCGWindowBounds); if(bd) CGRectMakeWithDictionaryRepresentation(bd,&r);
    char line[1024]; snprintf(line,sizeof line,"%d\t%s\t%s\t%.0f\t%.0f\t%.0f\t%.0f\t%d\t%d\n",pid,owner,title,r.origin.x,r.origin.y,r.size.width,r.size.height,layer,num);
    sbAdd(&b,line);
  }
  CFRelease(arr);
  return b.p?b.p:strdup("");
}

typedef struct { sbuf b; int count,max,maxDepth; CFAbsoluteTime deadline; } treeCtx;
static void axText(AXUIElementRef el,CFStringRef attr,char *out,size_t n){
  out[0]=0; CFTypeRef v=NULL;
  if(AXUIElementCopyAttributeValue(el,attr,&v)!=kAXErrorSuccess||!v) return;
  if(CFGetTypeID(v)==CFStringGetTypeID()) cfUTF8((CFStringRef)v,out,n);
  else if(CFGetTypeID(v)==CFNumberGetTypeID()){ double d=0; if(CFNumberGetValue((CFNumberRef)v,kCFNumberDoubleType,&d)) snprintf(out,n,"%g",d); }
  CFRelease(v);
}
static int interestingRole(const char *r){
  static const char *roles[]={"AXButton","AXTextField","AXTextArea","AXCheckBox","AXRadioButton","AXPopUpButton","AXMenuItem","AXMenuBarItem","AXMenuButton","AXLink","AXTab","AXSlider","AXComboBox","AXSwitch","AXWindow","AXSearchField","AXDisclosureTriangle",NULL};
  for(int i=0;roles[i];i++) if(strcmp(r,roles[i])==0) return 1;
  return 0;
}
// shown counts only emitted ancestors, so indentation stays meaningful.
static void axWalk(AXUIElementRef el,int depth,int shown,treeCtx *c){
  if(c->count>=c->max||depth>c->maxDepth||CFAbsoluteTimeGetCurrent()>c->deadline) return;
  char role[96],title[256],desc[256],value[256];
  axText(el,kAXRoleAttribute,role,sizeof role); axText(el,kAXTitleAttribute,title,sizeof title);
  axText(el,kAXDescriptionAttribute,desc,sizeof desc); axText(el,kAXValueAttribute,value,sizeof value);
  double x=0,y=0,w=0,h=0; CFTypeRef v=NULL; CGPoint p; CGSize s;
  if(AXUIElementCopyAttributeValue(el,kAXPositionAttribute,&v)==kAXErrorSuccess&&v){ if(AXValueGetValue((AXValueRef)v,kAXValueCGPointType,&p)){x=p.x;y=p.y;} CFRelease(v); }
  v=NULL;
  if(AXUIElementCopyAttributeValue(el,kAXSizeAttribute,&v)==kAXErrorSuccess&&v){ if(AXValueGetValue((AXValueRef)v,kAXValueCGSizeType,&s)){w=s.width;h=s.height;} CFRelease(v); }
  int emit=title[0]||desc[0]||value[0]||interestingRole(role);
  if(emit){
    char line[1500]; snprintf(line,sizeof line,"%d\t%s\t%s\t%s\t%s\t%.0f\t%.0f\t%.0f\t%.0f\n",shown,role,title,desc,value,x,y,w,h);
    sbAdd(&c->b,line); c->count++;
  }
  if(strcmp(role,"AXMenu")==0) return;
  CFTypeRef kids=NULL;
  if(AXUIElementCopyAttributeValue(el,kAXChildrenAttribute,&kids)!=kAXErrorSuccess||!kids) return;
  if(CFGetTypeID(kids)==CFArrayGetTypeID()){
    CFIndex n=CFArrayGetCount((CFArrayRef)kids);
    for(CFIndex i=0;i<n&&c->count<c->max;i++) axWalk((AXUIElementRef)CFArrayGetValueAtIndex((CFArrayRef)kids,i),depth+1,emit?shown+1:shown,c);
  }
  CFRelease(kids);
}
// Nine tab-separated fields per element: depth, role, title, description, value, x, y, w, h.
static char *uiTree(int pid,int maxNodes,int maxDepth,int *outPid){
  AXUIElementRef app=NULL;
  if(pid>0) app=AXUIElementCreateApplication(pid);
  else {
    AXUIElementRef sys=AXUIElementCreateSystemWide(); CFTypeRef f=NULL;
    AXError e=AXUIElementCopyAttributeValue(sys,kAXFocusedApplicationAttribute,&f); CFRelease(sys);
    if(e!=kAXErrorSuccess||!f) return NULL;
    app=(AXUIElementRef)f;
  }
  pid_t got=0; AXUIElementGetPid(app,&got); *outPid=(int)got;
  AXUIElementSetMessagingTimeout(app,1.0);
  treeCtx c; memset(&c,0,sizeof c); c.max=maxNodes; c.maxDepth=maxDepth; c.deadline=CFAbsoluteTimeGetCurrent()+4.0;
  axWalk(app,0,0,&c);
  CFRelease(app);
  return c.b.p?c.b.p:strdup("");
}
*/
import "C"
import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"
)

type nativeDriver struct{}

var errNeedAccessibility = errors.New("allow this program in macOS System Settings → Privacy & Security → Accessibility")

func (nativeDriver) Permissions() Permissions {
	return Permissions{true, C.screenAllowed() != 0, C.inputAllowed() != 0, "macOS"}
}

// Request asks macOS for "screen" or "accessibility" access, which adds this
// app to the matching System Settings list.
func (nativeDriver) Request(kind string) {
	switch kind {
	case "screen":
		C.requestScreen()
	case "accessibility":
		C.requestInput()
	}
}
func (nativeDriver) Displays() []Bounds {
	var out []Bounds
	for i := 0; i < 16; i++ {
		var x, y, w, h C.double
		if C.displayInfo(C.int(i), &x, &y, &w, &h) == 0 {
			break
		}
		out = append(out, Bounds{float64(x), float64(y), float64(w), float64(h)})
	}
	return out
}
func (d nativeDriver) Capture(ctx context.Context, display int) ([]byte, Bounds, error) {
	if C.screenAllowed() == 0 {
		return nil, Bounds{}, errors.New("allow this program in macOS System Settings → Privacy & Security → Screen & System Audio Recording, then restart it")
	}
	if display < 1 {
		display = 1
	}
	displays := d.Displays()
	if display > len(displays) {
		return nil, Bounds{}, fmt.Errorf("display %d not found; %d connected", display, len(displays))
	}
	f, err := os.CreateTemp("", "adapter-capture-*.png")
	if err != nil {
		return nil, Bounds{}, err
	}
	name := f.Name()
	f.Close()
	defer os.Remove(name)
	cmd := exec.CommandContext(ctx, "/usr/sbin/screencapture", "-x", "-D", strconv.Itoa(display), "-t", "png", name)
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, Bounds{}, fmt.Errorf("screen capture: %w: %s", err, b)
	}
	b, err := os.ReadFile(name)
	return b, displays[display-1], err
}

// CaptureWindow uses screencapture -l, which draws the window alone: windows in
// front of it do not show. -o leaves out its shadow so the image is its bounds.
func (nativeDriver) CaptureWindow(ctx context.Context, id int) ([]byte, error) {
	if C.screenAllowed() == 0 {
		return nil, errors.New("allow this program in macOS System Settings → Privacy & Security → Screen & System Audio Recording, then restart it")
	}
	f, err := os.CreateTemp("", "adapter-window-*.png")
	if err != nil {
		return nil, err
	}
	name := f.Name()
	f.Close()
	defer os.Remove(name)
	cmd := exec.CommandContext(ctx, "/usr/sbin/screencapture", "-x", "-o", "-l", strconv.Itoa(id), "-t", "png", name)
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("window capture: %w: %s", err, b)
	}
	return os.ReadFile(name)
}
func (nativeDriver) Input(ctx context.Context, a Action) error {
	if C.inputAllowed() == 0 {
		return errNeedAccessibility
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if C.corner() != 0 {
		return errors.New("emergency stop: the pointer is in a screen corner; move it away and retry")
	}
	flags := C.ulonglong(a.Flags)
	move := func(typ int, p []float64, button, count int) {
		C.mouseEvent(C.int(typ), C.double(p[0]), C.double(p[1]), C.int(button), C.int(count), flags)
	}
	switch a.Action {
	case "mouse_move":
		move(5, a.Coordinate, 0, 1)
	case "left_click", "right_click", "middle_click", "double_click", "triple_click":
		down, up, button := 1, 2, 0
		if a.Action == "right_click" {
			down, up, button = 3, 4, 1
		}
		if a.Action == "middle_click" {
			down, up, button = 25, 26, 2
		}
		count := 1
		switch a.Action {
		case "double_click":
			count = 2
		case "triple_click":
			count = 3
		}
		// Move first so the target sees a hover before the press, as a person would.
		move(5, a.Coordinate, 0, 1)
		for i := 1; i <= count; i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			move(down, a.Coordinate, button, i)
			move(up, a.Coordinate, button, i)
		}
	case "drag":
		move(1, a.Coordinate, 0, 1)
		pos := a.Coordinate
		defer func() { move(2, pos, 0, 1) }()
		for i := 1; i <= 20; i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if C.corner() != 0 {
				return errors.New("drag stopped by corner failsafe")
			}
			t := float64(i) / 20
			pos = []float64{a.Coordinate[0] + (a.To[0]-a.Coordinate[0])*t, a.Coordinate[1] + (a.To[1]-a.Coordinate[1])*t}
			move(6, pos, 0, 1)
			time.Sleep(10 * time.Millisecond)
		}
	case "scroll":
		if len(a.Coordinate) == 2 {
			move(5, a.Coordinate, 0, 1)
			time.Sleep(30 * time.Millisecond)
		}
		C.scrollEvent(C.int(a.Scroll[0]), C.int(a.Scroll[1]), flags)
	case "type":
		units := utf16.Encode([]rune(a.Text))
		for len(units) > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			n := min(len(units), 20)
			if n < len(units) && units[n-1] >= 0xD800 && units[n-1] <= 0xDBFF {
				n--
			}
			C.unicodeEvent((*C.UniChar)(unsafe.Pointer(&units[0])), C.int(n))
			units = units[n:]
		}
	case "key":
		var bits uint64
		code := -1
		for _, key := range a.Keys {
			k := strings.ToLower(key)
			if bit, ok := modifierBits[k]; ok {
				bits |= bit
				continue
			}
			v, ok := keyCodes[k]
			if !ok {
				return fmt.Errorf("unsupported key: %s", key)
			}
			if code != -1 {
				return errors.New("only one non-modifier key is allowed")
			}
			code = v
		}
		if code < 0 {
			return errors.New("a non-modifier key is required")
		}
		C.keyEvent(C.int(code), 1, C.ulonglong(bits))
		C.keyEvent(C.int(code), 0, 0)
	}
	return nil
}
func (nativeDriver) Clipboard(ctx context.Context, set *string) (string, error) {
	env := append(os.Environ(), "LANG=en_US.UTF-8")
	read := exec.CommandContext(ctx, "/usr/bin/pbpaste")
	read.Env = env
	current, err := read.Output()
	if err != nil {
		return "", fmt.Errorf("read clipboard: %w", err)
	}
	if set != nil {
		write := exec.CommandContext(ctx, "/usr/bin/pbcopy")
		write.Env = env
		write.Stdin = strings.NewReader(*set)
		if err := write.Run(); err != nil {
			return string(current), fmt.Errorf("write clipboard: %w", err)
		}
	}
	return string(current), nil
}

// rawWindows parses windowList output; windows are listed front to back.
func rawWindows() ([]Window, error) {
	raw := C.windowList()
	if raw == nil {
		return nil, errors.New("could not list windows")
	}
	defer C.free(unsafe.Pointer(raw))
	var out []Window
	for _, line := range strings.Split(C.GoString(raw), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 9 {
			continue
		}
		num := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
		pid, _ := strconv.Atoi(f[0])
		layer, _ := strconv.Atoi(f[7])
		id, _ := strconv.Atoi(f[8])
		out = append(out, Window{ID: id, PID: pid, App: f[1], Title: f[2], X: num(f[3]), Y: num(f[4]), Width: num(f[5]), Height: num(f[6]), Layer: layer})
	}
	return out, nil
}
func (nativeDriver) Windows(ctx context.Context) ([]Window, error) {
	all, err := rawWindows()
	if err != nil {
		return nil, err
	}
	var out []Window
	for _, w := range all {
		if w.Layer != 0 || w.Width <= 0 || w.Height <= 0 {
			continue
		}
		w.Frontmost = len(out) == 0
		out = append(out, w)
	}
	return out, nil
}
func (nativeDriver) UITree(ctx context.Context, app string, maxNodes, maxDepth int) (UITree, error) {
	if C.inputAllowed() == 0 {
		return UITree{}, errNeedAccessibility
	}
	windows, err := rawWindows()
	if err != nil {
		return UITree{}, err
	}
	pid, name := 0, ""
	if app != "" {
		for _, w := range windows {
			if w.Layer == 0 && strings.Contains(strings.ToLower(w.App), strings.ToLower(app)) {
				pid, name = w.PID, w.App
				break
			}
		}
		if pid == 0 {
			return UITree{}, fmt.Errorf("no window found for app %q", app)
		}
	}
	var got C.int
	raw := C.uiTree(C.int(pid), C.int(maxNodes), C.int(maxDepth), &got)
	if raw == nil {
		return UITree{}, errors.New("could not read the accessibility tree; check that the app is running and Accessibility is allowed")
	}
	defer C.free(unsafe.Pointer(raw))
	tree := UITree{App: name, PID: int(got)}
	if name == "" {
		for _, w := range windows {
			if w.PID == tree.PID {
				tree.App = w.App
				break
			}
		}
	}
	for _, line := range strings.Split(C.GoString(raw), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 9 {
			continue
		}
		num := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
		depth, _ := strconv.Atoi(f[0])
		tree.Nodes = append(tree.Nodes, UINode{Depth: depth, Role: f[1], Title: f[2], Description: f[3], Value: f[4], X: num(f[5]), Y: num(f[6]), Width: num(f[7]), Height: num(f[8])})
	}
	tree.Truncated = len(tree.Nodes) >= maxNodes
	return tree, nil
}
func (nativeDriver) OpenApp(ctx context.Context, name string) error {
	if out, err := exec.CommandContext(ctx, "/usr/bin/open", "-a", name).CombinedOutput(); err != nil {
		return fmt.Errorf("open %q: %s", name, strings.TrimSpace(string(out)))
	}
	return nil
}

var keyCodes = map[string]int{"a": 0, "s": 1, "d": 2, "f": 3, "h": 4, "g": 5, "z": 6, "x": 7, "c": 8, "v": 9, "b": 11, "q": 12, "w": 13, "e": 14, "r": 15, "y": 16, "t": 17, "1": 18, "2": 19, "3": 20, "4": 21, "6": 22, "5": 23, "=": 24, "9": 25, "7": 26, "-": 27, "8": 28, "0": 29, "]": 30, "o": 31, "u": 32, "[": 33, "i": 34, "p": 35, "enter": 36, "return": 36, "l": 37, "j": 38, "'": 39, "k": 40, ";": 41, "\\": 42, ",": 43, "/": 44, "n": 45, "m": 46, ".": 47, "tab": 48, "space": 49, "`": 50, "backspace": 51, "escape": 53, "esc": 53, "delete": 117, "home": 115, "end": 119, "pageup": 116, "pagedown": 121, "left": 123, "right": 124, "down": 125, "up": 126, "f1": 122, "f2": 120, "f3": 99, "f4": 118, "f5": 96, "f6": 97, "f7": 98, "f8": 100, "f9": 101, "f10": 109, "f11": 103, "f12": 111}
