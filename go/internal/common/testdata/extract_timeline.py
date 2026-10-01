#!/usr/bin/env python3
"""Turn a migrate log into the task timeline progress_tracker_replay_test replays (#621).

Usage: extract_timeline.py migrate.log > issue621_migrate_timeline.tsv

Output rows, tab separated, milliseconds from the first "starting phase" line:
  plan   <phase> <task>          a task in the resolved plan
  start  <ms>    <task>          the task began (end minus its logged duration)
  end    <ms>    <task>          the task completed
  frac   <ms>    <task> <d>/<n>  item-level progress, interpolated to 1s steps
  logged <ms>    <pct>  <eta_s>  what the tool itself printed, for comparison
  total  <ms>                    when the run finished

Only task names and timings are written. No project keys or hosts.
"""
import re
import sys
from datetime import datetime

TS = re.compile(r'^time=(\S+) ')
PHASE = re.compile(r'msg="starting phase" phase=(\d+)')
RUNNING = re.compile(r'msg="running task" task=(\S+)')
SUMMARY = re.compile(r'msg="task summary" task=([A-Za-z]+) .*duration=(\d+):(\d+):(\d+)\.(\d+)')
ITEMS = re.compile(r'msg="([A-Za-z]+) (\d+)/(\d+) - \d+%"')
STARTING = re.compile(r'msg="starting task" task=(\S+) items=(\d+)')
ETA = re.compile(r'Overall progress: (\d+)% - ETA: (\d+):(\d+):(\d+)')
FINAL = re.compile(r'msg="Command migrate: Duration')


def main(path):
    origin = None
    phase = 0
    plan, start, end, items, logged = [], {}, {}, {}, []
    total_ms = None
    phase_starts = []

    def ms(line):
        m = TS.match(line)
        return None if m is None else int((datetime.fromisoformat(m.group(1)) - origin).total_seconds() * 1000)

    with open(path, encoding='utf-8') as f:
        for line in f:
            m = TS.match(line)
            if m is None:
                continue
            if origin is None and PHASE.search(line):
                origin = datetime.fromisoformat(m.group(1))
            if origin is None:
                continue
            t = ms(line)
            if (p := PHASE.search(line)):
                phase = int(p.group(1))
                phase_starts.append(t)
            elif (r := RUNNING.search(line)):
                plan.append((phase, r.group(1)))
                start.setdefault(r.group(1), t)
            elif (s := SUMMARY.search(line)):
                h, mi, se, frac = (int(x) for x in s.groups()[1:])
                dur = ((h * 60 + mi) * 60 + se) * 1000 + frac
                end[s.group(1)] = t
                start[s.group(1)] = t - dur
            elif (st := STARTING.search(line)):
                items.setdefault(st.group(1), {'total': int(st.group(2)), 'pts': []})
            elif (i := ITEMS.search(line)):
                rec = items.setdefault(i.group(1), {'total': int(i.group(3)), 'pts': []})
                rec['total'] = int(i.group(3))
                rec['pts'].append((t, int(i.group(2))))
            elif (e := ETA.search(line)):
                h, mi, se = (int(x) for x in e.groups()[1:])
                logged.append((t, int(e.group(1)), (h * 60 + mi) * 60 + se))
            elif FINAL.search(line):
                total_ms = t

    phase_of = {task: ph for ph, task in plan}
    for task in start:
        if task not in end:
            # No duration logged: the task recorded no item outcomes. Bound it
            # by the start of the next phase, and assume it finished fast.
            ph = phase_of[task]
            bound = phase_starts[ph] if ph < len(phase_starts) else total_ms
            end[task] = min(start[task] + 1000, bound)

    out = []
    for ph, task in plan:
        out.append(('plan', ph, task))
    for task, t in start.items():
        out.append(('start', t, task))
    for task, t in end.items():
        out.append(('end', t, task))
    # migrateProjectHistory is a pseudo-task inside importProjectData. The
    # tool never marks it started or complete, so neither does the
    # timeline: only its item progress, spanning its host's lifetime.
    host = {'migrateProjectHistory': 'importProjectData'}
    if 'migrateProjectHistory' in items:
        out.append(('pseudo', 0, 'migrateProjectHistory', items['migrateProjectHistory']['total']))
    for task, rec in items.items():
        span = host.get(task, task)
        if span not in start:
            continue
        pts = [(start[span], 0)] + rec['pts'] + [(end[span], rec['total'])]
        for (t0, d0), (t1, d1) in zip(pts, pts[1:]):
            step = 1000
            t = t0
            while t < t1:
                d = d0 + (d1 - d0) * (t - t0) / max(1, t1 - t0)
                out.append(('frac', t, task, int(d), rec['total']))
                t += step
        out.append(('frac', pts[-1][0], task, rec['total'], rec['total']))
    for t, pct, eta in logged:
        out.append(('logged', t, pct, eta))
    out.append(('total', total_ms))

    rank = {'plan': 0, 'pseudo': 1}
    out.sort(key=lambda r: (rank.get(r[0], 2), r[1]))
    for r in out:
        if r[0] == 'frac':
            print(f'frac\t{r[1]}\t{r[2]}\t{r[3]}/{r[4]}')
        else:
            print('\t'.join(str(x) for x in r))


if __name__ == '__main__':
    main(sys.argv[1])
