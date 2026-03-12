// extension.js — Extensão GNOME Shell para expor a janela ativa via D-Bus.
// Registra o serviço org.gnome.Shell.Extensions.TrackerTime no session bus.
// O tracker-time consome via: gdbus call --session --dest org.gnome.Shell.Extensions.TrackerTime
//   --object-path /org/gnome/Shell/Extensions/TrackerTime
//   --method org.gnome.Shell.Extensions.TrackerTime.GetActiveWindow

import Gio from 'gi://Gio';
import { Extension } from 'resource:///org/gnome/shell/extensions/extension.js';

const DBUS_IFACE = `
<node>
  <interface name="org.gnome.Shell.Extensions.TrackerTime">
    <method name="GetActiveWindow">
      <arg type="s" direction="out" name="process_name"/>
      <arg type="s" direction="out" name="window_title"/>
    </method>
  </interface>
</node>`;

export default class TrackerTimeExtension extends Extension {
    _dbusId = null;
    _dbusImpl = null;

    enable() {
        this._dbusImpl = Gio.DBusExportedObject.wrapJSObject(DBUS_IFACE, this);
        this._dbusImpl.export(Gio.DBus.session, '/org/gnome/Shell/Extensions/TrackerTime');

        this._dbusId = Gio.DBus.session.own_name(
            'org.gnome.Shell.Extensions.TrackerTime',
            Gio.BusNameOwnerFlags.NONE,
            null,
            null,
        );
    }

    disable() {
        if (this._dbusImpl) {
            this._dbusImpl.unexport();
            this._dbusImpl = null;
        }
        if (this._dbusId) {
            Gio.DBus.session.unown_name(this._dbusId);
            this._dbusId = null;
        }
    }

    GetActiveWindow() {
        const focusWindow = global.display.focus_window;
        if (!focusWindow) {
            return ['', ''];
        }
        const processName = focusWindow.get_wm_class() || '';
        const windowTitle = focusWindow.get_title() || '';
        return [processName, windowTitle];
    }
}
