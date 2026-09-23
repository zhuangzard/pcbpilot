/**
 * PCB Pilot Connector — extension entry point.
 *
 * Bridges the pcbpilot Go daemon to the official `eda.*` API over a local
 * WebSocket. On startup it scans ports 61832-61841 (0xF188-0xF191), validates the daemon
 * handshake (service "pcbpilot"), registers a windowId, sends context, and
 * keeps a heartbeat. Incoming `request` frames are dispatched to typed actions
 * (see ./actions) and answered with `response` frames.
 *
 * Exported functions are wired to menu items in `extension.json`.
 */

import * as extensionConfig from '../extension.json';
import { ensureHeaderMenusVisible } from './header-menu';
import {
	bootstrapFromModuleLoad,
	getConnectionStatus,
	reconnect as transportReconnect,
	start as transportStart,
	stop as transportStop,
	deactivate as transportDeactivate,
} from './transport';

const STORAGE_KEY_AUTO_CONNECT = 'autoConnectEnabled';

// EasyEDA can evaluate a user-extension bundle without dispatching an
// activation event. Start from module scope as well; transport.start() keeps
// the normal activate() path idempotent.
bootstrapFromModuleLoad();
// The same skipped-activate path must still publish the recovery menu. This is
// best-effort and internally catches host errors; activate() retries below when
// the normal lifecycle callback does arrive.
void ensureHeaderMenusVisible(extensionConfig);

// ─── Lifecycle ────────────────────────────────────────────────────────

/**
 * Extension activation entry (supports onStartupFinished auto-start).
 *
 * @param status - activation reason (e.g. 'onStartupFinished')
 * @param arg - optional activation argument
 */
// eslint-disable-next-line unused-imports/no-unused-vars
export function activate(status?: 'onStartupFinished', arg?: string): void {
	// Declaring headerMenus in extension.json is not enough for a
	// user-installed extension; see ./header-menu for the host-side trace.
	void ensureHeaderMenusVisible(extensionConfig);
	transportStart('activate');
}

/**
 * Extension deactivation: tear down the connection without showing a toast.
 */
export function deactivate(): void {
	transportDeactivate();
}

// ─── Menu actions ─────────────────────────────────────────────────────

/**
 * Manually reconnect (menu item).
 */
export function reconnect(): void {
	transportReconnect();
}

/**
 * Stop the connection and cancel retries (menu item).
 */
export function stopConnection(): void {
	transportStop();
}

/**
 * Toggle the auto-connect-on-startup preference (menu item).
 */
export async function toggleAutoConnect(): Promise<void> {
	const current = eda.sys_Storage.getExtensionUserConfig(STORAGE_KEY_AUTO_CONNECT);
	const currentlyEnabled = current !== false;
	await eda.sys_Storage.setExtensionUserConfig(STORAGE_KEY_AUTO_CONNECT, !currentlyEnabled);
	const msgKey = currentlyEnabled ? 'Auto-Connect disabled' : 'Auto-Connect enabled';
	eda.sys_Message.showToastMessage(eda.sys_I18n.text(msgKey));
}

/**
 * Show the About dialog with the current connection status (menu item).
 */
export function about(): void {
	const status = getConnectionStatus();
	let statusLine: string;
	if (status.connected) {
		const portInfo = `Connected (port ${status.port})`;
		const windowInfo = status.windowId ? `\nWindow ID: ${status.windowId}` : '\nWindow ID: (not registered)';
		statusLine = `${portInfo}${windowInfo}`;
	}
	else if (status.connecting) {
		statusLine = 'Connecting...';
	}
	else {
		statusLine = 'Disconnected';
	}

	eda.sys_Dialog.showInformationMessage(
		`PCB Pilot Connector v${extensionConfig.version}\n${statusLine}`,
		'About',
	);
}
