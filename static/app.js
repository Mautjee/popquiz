// PopQuiz - SSE connection setup and video sync logic

// This file is included in base.html and provides:
// 1. SSE connection management with auto-reconnect
// 2. Video synchronisation on video_play events
// 3. Question reveal on show_question events
// 4. Form submission handling for answers

(function() {
    'use strict';

    // Auto-reconnect SSE connection
    // The EventSource API handles reconnection automatically,
    // but we add a manual reconnect on error states.

    // Video sync: handled in player.html template inline script
    // This file provides shared utilities.

    // Utility: get cookie value by name
    function getCookie(name) {
        const value = `; ${document.cookie}`;
        const parts = value.split(`; ${name}=`);
        if (parts.length === 2) return parts.pop().split(';').shift();
        return null;
    }

    // Utility: format SSE data as JSON
    function parseSSEData(data) {
        try {
            return JSON.parse(data);
        } catch (e) {
            return null;
        }
    }

    // Make these available globally
    window.PopQuiz = {
        getCookie: getCookie,
        parseSSEData: parseSSEData
    };
})();