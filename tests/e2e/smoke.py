"""Focus end to end: first password, the timer by mouse and keys, an
activity added, edited and renamed, the month and statistics, the command
line signed in with the password, connected apps and the lock."""
import os, re, subprocess, sys
from playwright.sync_api import sync_playwright, expect

BASE, OUT, CLI = os.environ["BASE"], os.environ["OUT"], os.environ["CLI"]
PASSWORD = "correct horse"
errors = []


def watch(page):
    page.on("console", lambda m: m.type == "error" and errors.append(m.text))
    page.on("pageerror", lambda e: errors.append(str(e)))


def shot(page, name):
    page.screenshot(path=f"{OUT}/{name}.png")


def key(page, k):
    """A key of the top row as an AZERTY keyboard sends it."""
    page.evaluate(f"window.dispatchEvent(new KeyboardEvent('keydown', {{ key: {k!r}, bubbles: true }}))")


def cli(*args, password=None):
    env = {**os.environ, "FOCUS_SERVER": ""}
    env.pop("FOCUS_TOKEN", None)
    r = subprocess.run([CLI, *args], input=password and password + "\n", capture_output=True, text=True, env=env, timeout=20)
    return r.returncode, r.stdout + r.stderr


def desktop(browser):
    ctx = browser.new_context(viewport={"width": 1400, "height": 860}, timezone_id="Europe/Paris")
    page = ctx.new_page()
    watch(page)
    page.goto(BASE)
    # First run: choose the password.
    pw = page.locator(".modal input[type=password]")
    expect(pw.first).to_be_visible()
    for i in range(pw.count()):
        pw.nth(i).fill(PASSWORD)
    pw.last.press("Enter")
    expect(page.locator("#timer-display")).to_have_text(re.compile(r"\d+:\d\d"))
    shot(page, "01-timer")

    # The project, then Space starts, pauses; x asks before cancelling.
    page.locator("#project-input").fill("Maths")
    expect(page.locator(".project-menu .pm-item", has_text="Create")).to_be_visible()
    page.locator("#project-input").press("Enter")
    expect(page.locator("#timer-project-name")).to_have_text("maths")
    page.locator("body").click(position={"x": 5, "y": 400})
    page.keyboard.press(" ")
    expect(page.locator("#label-toggle")).to_have_text("Pause")
    page.keyboard.press(" ")
    expect(page.locator("#label-toggle")).to_have_text("Resume")
    page.keyboard.press("x")
    expect(page.locator(".modal", has_text="Cancel session?")).to_be_visible()
    shot(page, "02-cancel")
    page.get_by_role("button", name="Cancel session").click()
    expect(page.locator("#label-toggle")).to_have_text("Start")

    # ? lists the keys.
    page.keyboard.press("?")
    expect(page.locator(".modal.help")).to_contain_text("Start / pause / resume")
    page.keyboard.press("Escape")

    # é (2 on AZERTY): the month; a adds an activity there.
    key(page, "é")
    expect(page.locator("#tab-month")).to_have_class(re.compile("active"))
    page.keyboard.press("a")
    dialog = page.locator(".modal", has_text="Add activity")
    expect(dialog).to_be_visible()
    dialog.locator("input[type=text]").fill("lab report")
    dialog.locator("input[type=datetime-local]").first.fill("2026-11-10T09:00")
    dialog.locator("input[type=datetime-local]").last.fill("2026-11-10T08:00")
    dialog.get_by_role("button", name="Add").click()
    expect(dialog.locator(".form-error")).to_contain_text("after the start")
    dialog.locator("input[type=datetime-local]").last.fill("2026-11-10T11:30")
    dialog.get_by_role("button", name="Add").click()
    expect(page.locator(".modal")).to_have_count(0)
    page.locator("#month-label").click()
    shot(page, "03-month")

    # Statistics: the project, renamed everywhere from its row.
    key(page, '"')
    expect(page.locator("#tab-stats")).to_have_class(re.compile("active"))
    page.locator("#stats-label").click()
    page.keyboard.press("h")
    expect(page.locator("#stats-label")).not_to_have_text(re.compile("November"))
    page.keyboard.press("t")
    cell = page.locator(".project-cell", has_text="lab report")
    expect(cell).to_be_visible()
    shot(page, "04-stats")
    cell.click()
    rename = page.locator(".modal", has_text="everywhere")
    rename.locator("input").fill("labs")
    rename.get_by_role("button", name="Rename").click()
    expect(page.locator(".project-cell", has_text="labs")).to_be_visible()

    # The command line signs in with the password and drives the timer.
    code, out = cli("login", BASE, password="wrong")
    assert code != 0 and "incorrect" in out, out
    code, out = cli("login", BASE, password=PASSWORD)
    assert code == 0, out
    code, out = cli("start", "-m", "free", "-p", "cli")
    assert code == 0 and "free-running" in out, out
    key(page, "&")
    expect(page.locator("#timer-project-name")).to_have_text("cli")
    expect(page.locator("#phase-badge")).to_have_text("Stopwatch")
    code, out = cli("status", "--json")
    assert code == 0 and '"project":"cli"' in out, out
    page.keyboard.press("s")
    expect(page.locator("#label-toggle")).to_have_text("Start")

    # Connected apps: the command line, which can be disconnected.
    page.locator("#btn-more-menu").click()
    page.get_by_text("Connected apps").click()
    row = page.locator(".app-row", has_text="Command line")
    expect(row).to_be_visible()
    shot(page, "05-apps")
    row.get_by_role("button", name="Disconnect").click()
    expect(page.locator(".app-row")).to_have_count(0)
    code, out = cli("status")
    assert "offline" in out.lower(), out
    page.keyboard.press("Escape")

    # Lock: the password again.
    page.locator(".lock-button").click()
    page.get_by_text("Lock", exact=True).click()
    expect(page.locator(".modal input[type=password]")).to_be_visible()
    page.locator(".modal input[type=password]").fill(PASSWORD)
    page.locator(".modal input[type=password]").press("Enter")
    expect(page.locator(".modal")).to_have_count(0)
    ctx.close()


def phone(browser):
    ctx = browser.new_context(viewport={"width": 390, "height": 844}, device_scale_factor=2, is_mobile=True, has_touch=True)
    page = ctx.new_page()
    watch(page)
    page.goto(BASE)
    page.locator(".modal input[type=password]").fill(PASSWORD)
    page.locator(".modal input[type=password]").press("Enter")
    expect(page.locator("#timer-display")).to_be_visible()
    assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth"), "timer wider than the phone"
    shot(page, "06-phone-timer")
    page.locator(".nav-tab[data-tab=tab-month]").tap()
    page.locator("#btn-add-activity").tap()
    footer = page.locator(".modal-footer")
    expect(footer).to_be_visible()
    box = footer.bounding_box()
    assert box["y"] > 700, f"Cancel / Add not at the bottom: {box}"
    shot(page, "07-phone-add")
    ctx.close()


with sync_playwright() as p:
    browser = p.chromium.launch()
    try:
        desktop(browser)
        phone(browser)
    finally:
        browser.close()

if errors:
    print("\n".join(errors))
    sys.exit(1)
print("e2e ok")
