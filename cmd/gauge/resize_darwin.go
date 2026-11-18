//go:build darwin && cgo && !purego

package main

/*
#cgo CFLAGS: -mmacosx-version-min=10.13 -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework QuartzCore

#import <Cocoa/Cocoa.h>
#import <QuartzCore/QuartzCore.h>
#include <dispatch/dispatch.h>
#include <stdlib.h>

typedef struct {
	void *win;
	int width;
	int height;
} GaugeResizeReq;

static void gaugeResizeOnMain(void *ctx) {
	GaugeResizeReq *req = (GaugeResizeReq *)ctx;
	@autoreleasepool {
		NSWindow *win = (NSWindow *)req->win;
		int w = req->width;
		int h = req->height;
		free(req);
		if (!win) {
			return;
		}
		NSRect cur = [win frame];
		CGFloat top = NSMaxY(cur);
		NSRect target = NSMakeRect(cur.origin.x, top - (CGFloat)h, (CGFloat)w, (CGFloat)h);
		if (NSEqualRects(cur, target)) {
			return;
		}
		BOOL reduceMotion = [[NSWorkspace sharedWorkspace] accessibilityDisplayShouldReduceMotion];
		if (![win isVisible] || reduceMotion) {
			[win setFrame:target display:YES animate:NO];
			return;
		}
		[NSAnimationContext runAnimationGroup:^(NSAnimationContext *animCtx) {
			animCtx.duration = 0.18;
			animCtx.timingFunction = [CAMediaTimingFunction functionWithControlPoints:0.22 :1.0 :0.36 :1.0];
			animCtx.allowsImplicitAnimation = YES;
			[[win animator] setFrame:target display:YES];
		} completionHandler:nil];
	}
}

static void gaugeResizePopover(void *nsWindow, int width, int height) {
	if (!nsWindow) {
		return;
	}
	GaugeResizeReq *req = (GaugeResizeReq *)malloc(sizeof(GaugeResizeReq));
	if (!req) {
		return;
	}
	req->win = nsWindow;
	req->width = width;
	req->height = height;
	dispatch_async_f(dispatch_get_main_queue(), req, gaugeResizeOnMain);
}
*/
import "C"

import "github.com/wailsapp/wails/v3/pkg/application"

// resizePopover anchors the top edge below the menu bar while smoothly gliding
// the bottom edge to match the active page's height.
func resizePopover(w *application.WebviewWindow, width, height int) {
	nw := w.NativeWindow()
	if nw == nil {
		w.SetSize(width, height)
		return
	}
	C.gaugeResizePopover(nw, C.int(width), C.int(height))
}
