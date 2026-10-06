    document.getElementById('lead-form')?.addEventListener('submit', async (e) => {
      e.preventDefault();
      const email = e.target.email.value;
      if (!email) return;

      const btn = e.target.querySelector('button');
      btn.textContent = 'Sending...';
      btn.disabled = true;

      try {
        const resp = await fetch('/v1/leads', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ email })
        });
        if (resp.ok) {
          alert("Thanks — we'll email you when Team is ready.");
          e.target.reset();
        } else {
          alert('Something went wrong. Please try again later.');
        }
      } catch (err) {
        window.location.href = 'mailto:felix@felixgeelhaar.de?subject=Team%20waitlist&body=Email: ' + encodeURIComponent(email);
      } finally {
        btn.textContent = 'Join waitlist';
        btn.disabled = false;
      }
    });

    document.querySelectorAll('.cta-primary, .cta-secondary, .price-cta').forEach(el => {
      el.addEventListener('click', () => {
        if (window.gtag) {
          gtag('event', 'cta_click', {
            event_category: 'Landing',
            event_label: el.textContent.trim()
          });
        }
      });
    });
