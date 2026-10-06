#!/usr/bin/env python3
"""Compare two ReadyRig builds on the same agent tasks.

For each build it starts an isolated instance (own data dir, own ports, no
Chrome, no tunnel) and runs the tasks below over MCP, the way an agent would,
using the best strategy the build offers (batching, long waits and list_tasks
wait are used only when the build has them). It reports, per task, the number
of requests, the bytes returned (a proxy for tokens: about 3.5 bytes/token), the
wall time, and whether the job completed.

Usage:
  scripts/bench-tools.py OLD_BINARY NEW_BINARY [--quick]   compare two builds
  scripts/bench-tools.py --check BINARY                    guard: fail if BINARY exceeds scripts/bench-baseline.json
  scripts/bench-tools.py --write-baseline BINARY           record the budgets from BINARY (a deliberate act)

The guard exists so that no later feature quietly makes agents pay more tokens,
make more requests, or lose jobs that used to finish. If a change must raise a
budget, rewrite the baseline in the same commit and explain why in changelog/unreleased.md.
"""
import argparse, json, os, re, shutil, subprocess, sys, tempfile, threading, time, urllib.request

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BASELINE = os.path.join(REPO, 'scripts', 'bench-baseline.json')
HEADROOM = 1.05


def make_workspace(ws):
    """A fixed workspace, so budgets do not drift as the repository's own files change."""
    def text(name, size):
        out, n = [], 0
        while n < size:
            line = '%s line %d: the quick brown fox jumps over the lazy dog\n' % (name, len(out) + 1)
            out.append(line)
            n += len(line)
        return ''.join(out)
    for name, size in (('README.md', 47000), ('internal/server/server.go', 17000), ('internal/harness/registry.go', 12000), ('internal/computer/computer.go', 25000)):
        path = os.path.join(ws, name)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        open(path, 'w').write(text(name, size))
    for i in range(30):
        path = os.path.join(ws, 'internal', 'harness', 'file%02d.go' % i)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        open(path, 'w').write(''.join('func name%d_%d() {}\n' % (i, j) for j in range(40)))
BYTES_PER_TOKEN = 3.5


class Server:
    def __init__(self, label, binary, port, workspace, root):
        self.label, self.data = label, os.path.join(root, 'data-' + label)
        self.log = open(os.path.join(root, label + '.log'), 'w')
        self.proc = subprocess.Popen([binary, '--data-dir', self.data, 'serve', '--foreground', '--workspace', workspace, '--allow-shell', '--allow-computer', '--no-chrome', '--no-update', '--gateway', '127.0.0.1:%d' % (port + 1), '--ui', '127.0.0.1:%d' % port], stdout=self.log, stderr=self.log, start_new_session=True)
        self.base = None
        for _ in range(100):
            time.sleep(0.2)
            m = re.search(r'Agent API: (\S+)', open(self.log.name).read())
            if m:
                self.base = m.group(1)
                break
        if not self.base:
            raise SystemExit('%s did not start' % label)

    def stop(self):
        try:
            os.killpg(self.proc.pid, 15)
        except ProcessLookupError:
            pass


class Client:
    """One MCP session; counts requests and bytes of everything the agent receives."""

    def __init__(self, base):
        self.base, self.sid, self.n, self.calls, self.bytes = base, None, 0, 0, 0
        init = self.rpc('initialize', {'protocolVersion': '2025-06-18', 'clientInfo': {'name': 'bench'}})
        self.instructions = len(init['instructions'])
        tools = self.rpc('tools/list')['tools']
        self.tools = {t['name'] for t in tools}
        self.schemas = {t['name']: t['inputSchema'].get('properties', {}) for t in tools}
        self.tools_list_bytes = len(json.dumps(tools, separators=(',', ':')))

    def rpc(self, method, params=None):
        self.n += 1
        body = {'jsonrpc': '2.0', 'id': self.n, 'method': method}
        if params is not None:
            body['params'] = params
        req = urllib.request.Request(self.base + '/mcp', json.dumps(body).encode(), {'Content-Type': 'application/json'})
        if self.sid:
            req.add_header('Mcp-Session-Id', self.sid)
        with urllib.request.urlopen(req, timeout=180) as r:
            self.sid = r.headers.get('Mcp-Session-Id') or self.sid
            return json.loads(r.read())['result']

    def call(self, name, args):
        """Returns (text, is_error). Counts one request and the text bytes received."""
        res = self.rpc('tools/call', {'name': name, 'arguments': args})
        text = ''.join(c['text'] for c in res['content'] if c['type'] == 'text')
        self.calls += 1
        self.bytes += len(text)
        return text, res.get('isError', False)

    def has(self, tool):
        return tool in self.tools

    def supports(self, tool, param):
        return param in self.schemas.get(tool, {})

    def max_wait(self, tool):
        """The longest wait the build allows, read from its schema like an agent would."""
        return self.schemas.get(tool, {}).get('yield_time_ms', {}).get('maximum', 10000)


def finished(text):
    return '[running' not in text and ('[exit code' in text or '[terminated' in text or '[timed out]' in text or '"exit_code"' in text)


def session_of(text):
    m = re.search(r'session_id[=":\s]+([0-9a-f]{24})', text)
    return m.group(1) if m else None


def wait_for_completion(c, sid):
    """Poll with the longest wait the build allows until the command ends."""
    wait = c.max_wait('write_stdin')
    while True:
        text, err = c.call('write_stdin', {'session_id': sid, 'yield_time_ms': wait})
        if finished(text) or (err and 'session' in text):
            return text


def task_survey(c):
    files = ['README.md', 'internal/server/server.go', 'internal/harness/registry.go', 'internal/computer/computer.go']
    reads = [('read_file', {'path': p}) for p in files]
    extra = [('search_files', {'query': 'func ', 'path': 'internal/harness'}), ('list_directory', {'path': 'internal'})]
    if c.has('batch'):
        c.call('batch', {'calls': [{'tool': t, 'arguments': a} for t, a in reads + extra]})
    else:
        for t, a in reads + extra:
            c.call(t, a)
    return {}


def task_big_output(c):
    c.call('exec_command', {'command': 'i=0; while [ $i -lt 20000 ]; do echo "padding line number $i"; i=$((i+1)); done', 'yield_time_ms': 8000})
    return {}


def task_small_ops(c):
    c.call('write_file', {'path': 'bench/a.txt', 'content': 'one\ntwo\nthree\n'})
    c.call('edit_file', {'path': 'bench/a.txt', 'old_string': 'two', 'new_string': '2'})
    c.call('read_file', {'path': 'bench/a.txt'})
    c.call('exec_command', {'command': 'echo hello'})
    c.call('exec_command', {'command': 'ls /nonexistent-bench-dir'})
    return {}


def task_wait_silent_job(c, seconds):
    t, _ = c.call('exec_command', {'command': 'sleep %d; echo silent-done' % seconds, 'yield_time_ms': 500, 'background': True})
    sid = session_of(t)
    text = wait_for_completion(c, sid)
    return {'completed': 'silent-done' in text}


def task_two_jobs(c, a, b):
    ids = []
    for secs in (a, b):
        t, _ = c.call('exec_command', {'command': 'sleep %d; echo job-%d-done' % (secs, secs), 'yield_time_ms': 300, 'background': True})
        ids.append(session_of(t))
    if c.supports('list_tasks', 'wait'):
        while True:
            text, _ = c.call('list_tasks', {'wait': 'all', 'yield_time_ms': c.max_wait('list_tasks')})
            if not re.search(r'running \d', text):
                return {'completed': True}
    done = 0
    for sid in ids:
        done += 'done' in wait_for_completion(c, sid)
    return {'completed': done == 2}


def task_long_default_timeout(c, seconds):
    t, _ = c.call('exec_command', {'command': 'sleep %d; echo finished-ok' % seconds, 'yield_time_ms': 1000})
    sid = session_of(t)
    text = wait_for_completion(c, sid)
    return {'completed': 'finished-ok' in text}


def timed(fn, c, *args):
    c0, b0, t0 = c.calls, c.bytes, time.time()
    extra = fn(c, *args)
    return dict({'calls': c.calls - c0, 'bytes': c.bytes - b0, 'seconds': round(time.time() - t0, 1)}, **extra)


def run_build(label, binary, port, workspace, root, quick):
    srv = Server(label, binary, port, workspace, root)
    out = {}
    try:
        main = Client(srv.base)
        out['session setup'] = {'calls': 2, 'bytes': main.instructions + main.tools_list_bytes, 'tools': len(main.tools)}
        long_results, threads = {}, []
        silent, pair, longsleep = (25, (10, 14), 40) if quick else (50, (20, 25), 70)
        jobs = [('wait for a silent %ds job' % silent, task_wait_silent_job, (silent,)), ('wait for two jobs (%ds, %ds)' % pair, task_two_jobs, pair), ('%ds command, default timeout' % longsleep, task_long_default_timeout, (longsleep,))]
        for name, fn, args in jobs:
            def work(name=name, fn=fn, args=args):
                try:
                    long_results[name] = timed(fn, Client(srv.base), *args)
                except Exception as e:  # report, do not hide
                    long_results[name] = {'calls': 0, 'bytes': 0, 'seconds': 0, 'completed': False, 'error': repr(e)[:80]}
            t = threading.Thread(target=work)
            t.start()
            threads.append(t)
        for name, fn in [('survey: 4 files + search + list', task_survey), ('508 KB command output', task_big_output), ('small ops (write, edit, read, 2 commands)', task_small_ops)]:
            out[name] = timed(fn, Client(srv.base))
        for t in threads:
            t.join()
        out.update(long_results)
    finally:
        srv.stop()
    return out


def run_one(binary):
    root = tempfile.mkdtemp(prefix='readyrig-bench-')
    ws = os.path.join(root, 'ws')
    os.makedirs(ws)
    make_workspace(ws)
    try:
        return run_build('guard', binary, 29771 + (os.getpid() % 100) * 2, ws, root, False)
    finally:
        shutil.rmtree(root, ignore_errors=True)


def guard(a):
    binary = a.check or a.write_baseline
    results = run_one(binary)
    if a.write_baseline:
        tasks = {}
        for name, r in results.items():
            b = {'max_bytes': int(r['bytes'] * HEADROOM) + 16}
            if name != 'session setup':
                b['max_calls'] = r['calls']
                if 'completed' in r:
                    b['must_complete'] = bool(r['completed'])
                if r['seconds'] >= 20:
                    b['max_seconds'] = int(r['seconds']) + 20
            tasks[name] = b
        json.dump({'_about': 'Budgets for scripts/bench-tools.py --check. Rewrite only on purpose, in the same commit as the change that needs it, and say why in changelog/unreleased.md.', 'tasks': tasks}, open(BASELINE, 'w'), indent=1)
        print('wrote', BASELINE)
        return 0
    baseline = json.load(open(BASELINE))['tasks']
    failures = []
    print('%-44s %-22s %s' % ('task', 'calls / bytes (budget)', 'result'))
    for name, b in baseline.items():
        r = results.get(name)
        if r is None:
            failures.append('%s: task missing from the run' % name)
            continue
        problems = []
        if 'max_calls' in b and r['calls'] > b['max_calls']:
            problems.append('%d calls, budget %d' % (r['calls'], b['max_calls']))
        if r['bytes'] > b['max_bytes']:
            problems.append('%d bytes, budget %d' % (r['bytes'], b['max_bytes']))
        if b.get('must_complete') and not r.get('completed'):
            problems.append('the job did not complete')
        if 'max_seconds' in b and r['seconds'] > b['max_seconds']:
            problems.append('%ss, budget %ss' % (r['seconds'], b['max_seconds']))
        calls = ('%d/%s' % (r['calls'], b.get('max_calls', '-')))
        print('%-44s %-22s %s' % (name, '%s  %d/%d' % (calls, r['bytes'], b['max_bytes']), 'ok' if not problems else 'FAIL: ' + '; '.join(problems)))
        failures += ['%s: %s' % (name, p) for p in problems]
    if failures:
        print('\nGUARD FAILED (%d):' % len(failures))
        for f in failures:
            print('  -', f)
        return 1
    print('\nguard passed')
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('old', nargs='?')
    ap.add_argument('new', nargs='?')
    ap.add_argument('--check', metavar='BINARY', help='guard mode: fail if the build exceeds the baseline budgets')
    ap.add_argument('--write-baseline', metavar='BINARY', help='write the baseline budgets from this build')
    ap.add_argument('--quick', action='store_true', help='shorter jobs (about 25 s instead of 70 s)')
    ap.add_argument('--json', help='also write raw results to this file')
    a = ap.parse_args()
    if a.check or a.write_baseline:
        return guard(a)
    if not (a.old and a.new):
        ap.error('give OLD and NEW binaries, or use --check / --write-baseline')
    root = tempfile.mkdtemp(prefix='readyrig-bench-')
    ws = os.path.join(root, 'ws')
    os.makedirs(ws)
    make_workspace(ws)
    results = {}
    threads = []
    for label, binary, port in (('old', a.old, 29331), ('new', a.new, 29441)):
        def go(label=label, binary=binary, port=port):
            results[label] = run_build(label, binary, port, ws, root, a.quick)
        t = threading.Thread(target=go)
        t.start()
        threads.append(t)
    for t in threads:
        t.join()
    shutil.rmtree(root, ignore_errors=True)
    if a.json:
        json.dump(results, open(a.json, 'w'), indent=1)
    print('%-44s %-26s %-26s %s' % ('task', 'old: calls / ~tokens / sec', 'new: calls / ~tokens / sec', 'completed old -> new'))
    for task in results['old']:
        o, n = results['old'][task], results['new'][task]
        fmt = lambda r: '%d / %d / %s' % (r['calls'], round(r['bytes'] / BYTES_PER_TOKEN), r['seconds']) if 'calls' in r else ''
        done = ''
        if 'completed' in o or 'completed' in n:
            done = '%s -> %s' % (o.get('completed'), n.get('completed'))
        if task == 'session setup':
            print('%-44s %-26s %-26s tools %d -> %d' % (task, '- / %d / -' % round(o['bytes'] / BYTES_PER_TOKEN), '- / %d / -' % round(n['bytes'] / BYTES_PER_TOKEN), o['tools'], n['tools']))
        else:
            print('%-44s %-26s %-26s %s' % (task, fmt(o), fmt(n), done))


if __name__ == '__main__':
    sys.exit(main() or 0)
