/* Early theme resolution: runs before first paint to avoid a flash of the wrong theme. */
(function () {
    'use strict';
    var root = document.documentElement;
    try {
        var pref = localStorage.getItem('pycloud.theme') || 'graphite';
        var resolved = pref;
        if (pref === 'system') {
            resolved = window.matchMedia('(prefers-color-scheme: light)').matches ? 'paper' : 'graphite';
        }
        if (resolved !== 'graphite' && resolved !== 'paper' && resolved !== 'terminal') {
            resolved = 'graphite';
        }
        root.setAttribute('data-theme', resolved);
        root.setAttribute('data-theme-pref', pref);
        if (localStorage.getItem('pycloud.motion') === 'reduced') {
            root.setAttribute('data-motion', 'reduced');
        }
    } catch (err) {
        root.setAttribute('data-theme', 'graphite');
    }
})();
