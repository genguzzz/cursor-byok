#import <Cocoa/Cocoa.h>
#import <stdlib.h>
#import "_cgo_export.h"

@interface MenuHandler : NSObject
@end

@implementation MenuHandler
- (void)startAction:(id)sender      { menuCallback(1); }
- (void)stopAction:(id)sender       { menuCallback(2); }
- (void)quitAction:(id)sender       { menuCallback(3); }
- (void)toggleProxyAction:(id)sender { menuCallback(4); }
@end

static NSStatusItem   *g_statusItem    = nil;
static NSMenuItem     *g_startItem     = nil;
static NSMenuItem     *g_stopItem      = nil;
static NSMenuItem     *g_statusDisplay = nil;
static NSMenuItem     *g_proxyItem     = nil;
static MenuHandler    *g_handler       = nil;

void setupMenubar() {
    [NSApplication sharedApplication];
    [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];

    NSStatusBar *bar = [NSStatusBar systemStatusBar];
    g_statusItem = [bar statusItemWithLength:NSVariableStatusItemLength];

    NSImage *icon = [NSImage imageWithSystemSymbolName:@"network"
                             accessibilityDescription:@"Cursor Local Assistant"];
    if (icon != nil) {
        [icon setTemplate:YES];
        [g_statusItem.button setImage:icon];
    } else {
        [g_statusItem.button setTitle:@"C"];
    }

    g_handler = [[MenuHandler alloc] init];
    NSMenu *menu = [[NSMenu alloc] init];

    g_startItem = [menu addItemWithTitle:@"开启拦截"
                                  action:@selector(startAction:)
                           keyEquivalent:@""];
    [g_startItem setTarget:g_handler];

    g_stopItem = [menu addItemWithTitle:@"关闭拦截"
                                 action:@selector(stopAction:)
                          keyEquivalent:@""];
    [g_stopItem setEnabled:NO];
    [g_stopItem setTarget:g_handler];

    [menu addItem:[NSMenuItem separatorItem]];

    g_proxyItem = [menu addItemWithTitle:@"使用代理 (9090)"
                                  action:@selector(toggleProxyAction:)
                           keyEquivalent:@""];
    [g_proxyItem setTarget:g_handler];

    [menu addItem:[NSMenuItem separatorItem]];

    g_statusDisplay = [menu addItemWithTitle:@"状态: 已停止"
                                      action:nil
                               keyEquivalent:@""];
    [g_statusDisplay setEnabled:NO];

    [menu addItem:[NSMenuItem separatorItem]];

    NSMenuItem *quitItem = [menu addItemWithTitle:@"退出"
                                           action:@selector(quitAction:)
                                    keyEquivalent:@"q"];
    [quitItem setTarget:g_handler];

    [g_statusItem setMenu:menu];
}

void runEventLoop() {
    [NSApp run];
}

void stopEventLoop() {
    [NSApp stop:nil];
    NSEvent *event = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
                                        location:NSZeroPoint
                                   modifierFlags:0
                                       timestamp:0
                                    windowNumber:0
                                         context:nil
                                         subtype:0
                                           data1:0
                                           data2:0];
    [NSApp postEvent:event atStart:NO];
}

void updateMenubarStatus(const char *status, int running) {
    NSString *title = [[NSString alloc] initWithUTF8String:status];
    dispatch_async(dispatch_get_main_queue(), ^{
        if (g_statusDisplay) {
            [g_statusDisplay setTitle:title];
        }
        if (g_startItem) { [g_startItem setEnabled:!running]; }
        if (g_stopItem)  { [g_stopItem  setEnabled:running]; }
    });
}

void setProxyMenuItemEnabled(int enabled) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (g_proxyItem) {
            [g_proxyItem setState:(enabled ? NSControlStateValueOn : NSControlStateValueOff)];
        }
    });
}
