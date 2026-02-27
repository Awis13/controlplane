// CSRF: send token with every HTMX request
document.addEventListener('htmx:configRequest', (e) => {
  const meta = document.querySelector('meta[name="csrf-token"]');
  if (meta) {
    e.detail.headers['X-CSRF-Token'] = meta.getAttribute('content');
  }
});

// Toast notification system
document.addEventListener('alpine:init', () => {
  Alpine.store('toasts', {
    items: [],
    add(msg, type = 'success') {
      const id = Date.now();
      this.items.push({ id, msg, type, leaving: false });
      setTimeout(() => this.remove(id), 4000);
    },
    remove(id) {
      const item = this.items.find(i => i.id === id);
      if (item) {
        item.leaving = true;
        setTimeout(() => {
          this.items = this.items.filter(i => i.id !== id);
        }, 300);
      }
    }
  });

  Alpine.store('sidebar', {
    collapsed: localStorage.getItem('sidebar-collapsed') === 'true',
    toggle() {
      this.collapsed = !this.collapsed;
      localStorage.setItem('sidebar-collapsed', this.collapsed);
    }
  });
});

// Listen for HX-Trigger toast events from HTMX responses
document.addEventListener('showToast', (e) => {
  const detail = e.detail || {};
  Alpine.store('toasts').add(detail.message || 'Done', detail.type || 'success');
});

document.addEventListener('showError', (e) => {
  const detail = e.detail || {};
  Alpine.store('toasts').add(detail.message || 'Error', 'error');
});

// HTMX event: listen for HX-Trigger headers
document.body.addEventListener('htmx:afterRequest', (e) => {
  const trigger = e.detail.xhr?.getResponseHeader('HX-Trigger');
  if (!trigger) return;
  try {
    const events = JSON.parse(trigger);
    if (events.showToast) {
      Alpine.store('toasts').add(events.showToast.message, events.showToast.type || 'success');
    }
    if (events.showError) {
      Alpine.store('toasts').add(events.showError.message, 'error');
    }
  } catch {
    // HX-Trigger might be a simple event name, not JSON
  }
});

// Confirmation modal
function confirmAction(message) {
  return confirm(message);
}
