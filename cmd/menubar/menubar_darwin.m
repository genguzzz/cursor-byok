#import <Cocoa/Cocoa.h>
#import <stdlib.h>
#import "_cgo_export.h"

@interface MenuHandler : NSObject
@end

@implementation MenuHandler
- (void)localModeAction:(id)sender  { menuCallback(1); }
- (void)quitAction:(id)sender       { menuCallback(3); }
- (void)toggleProxyAction:(id)sender { menuCallback(4); }
- (void)restoreAuthAction:(id)sender { menuCallback(5); }
- (void)toggleDebugAction:(id)sender { menuCallback(6); }
- (void)toggleDebugLogAction:(id)sender { menuCallback(7); }
@end

static NSStatusItem   *g_statusItem    = nil;
static NSMenuItem     *g_localModeItem = nil;
static NSMenuItem     *g_statusDisplay = nil;
static NSMenuItem     *g_proxyItem     = nil;
static NSMenuItem     *g_debugItem     = nil;
static NSMenuItem     *g_debugLogItem  = nil;
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

    // 单一开关：开启时勾选，关闭时取消勾选；启动/关闭过程中短暂置灰。
    g_localModeItem = [menu addItemWithTitle:@"本地模式"
                                      action:@selector(localModeAction:)
                               keyEquivalent:@""];
    [g_localModeItem setTarget:g_handler];
    [g_localModeItem setState:NSControlStateValueOff];

    [menu addItem:[NSMenuItem separatorItem]];

    g_proxyItem = [menu addItemWithTitle:@"模型出站代理 (9090)"
                                  action:@selector(toggleProxyAction:)
                           keyEquivalent:@""];
    [g_proxyItem setTarget:g_handler];

    [menu addItem:[NSMenuItem separatorItem]];

    g_statusDisplay = [menu addItemWithTitle:@"状态: 已停止"
                                      action:nil
                               keyEquivalent:@""];
    [g_statusDisplay setEnabled:NO];

    NSMenuItem *restoreItem = [menu addItemWithTitle:@"强制恢复账号与设置"
                                              action:@selector(restoreAuthAction:)
                                       keyEquivalent:@""];
    [restoreItem setTarget:g_handler];

    [menu addItem:[NSMenuItem separatorItem]];

    g_debugItem = [menu addItemWithTitle:@"🪲 调试模式 (9092→9091)"
                                  action:@selector(toggleDebugAction:)
                           keyEquivalent:@""];
    [g_debugItem setTarget:g_handler];
    [g_debugItem setState:NSControlStateValueOff];

    g_debugLogItem = [menu addItemWithTitle:@"调试日志 (app.log)"
                                     action:@selector(toggleDebugLogAction:)
                              keyEquivalent:@""];
    [g_debugLogItem setTarget:g_handler];
    [g_debugLogItem setState:NSControlStateValueOff];

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

void updateMenubarStatus(const char *status, int running, int busy) {
    NSString *title = [[NSString alloc] initWithUTF8String:status];
    dispatch_async(dispatch_get_main_queue(), ^{
        if (g_statusDisplay) {
            [g_statusDisplay setTitle:title];
        }
        if (g_localModeItem) {
            [g_localModeItem setState:(running ? NSControlStateValueOn : NSControlStateValueOff)];
            // 启停过程中置灰，避免连点；稳态始终可点以切换。
            [g_localModeItem setEnabled:!busy];
        }
    });
}

void setProxyMenuItemEnabled(int enabled) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (g_proxyItem) {
            [g_proxyItem setState:(enabled ? NSControlStateValueOn : NSControlStateValueOff)];
        }
    });
}

void setDebugMenuItemEnabled(int enabled) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (g_debugItem) {
            [g_debugItem setState:(enabled ? NSControlStateValueOn : NSControlStateValueOff)];
        }
    });
}

void setDebugLogMenuItemEnabled(int enabled) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (g_debugLogItem) {
            [g_debugLogItem setState:(enabled ? NSControlStateValueOn : NSControlStateValueOff)];
        }
    });
}
