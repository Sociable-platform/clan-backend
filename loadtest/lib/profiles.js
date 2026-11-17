// How many players, for how long. A scenario ramps through the stages of one profile, chosen
// with LOADTEST_PROFILE:
//
//   short  (default)  up to 10 players for about two minutes: to check that a script works
//   steps             10, 50, 200, then 500 players, two minutes each: the real measurement
//
// Each step ramps up for 30 seconds and then holds, so the numbers of a step can be read off the
// run while it holds steady.

const hold = (players, duration) => [
  { duration: '30s', target: players },
  { duration, target: players },
];

const PROFILES = {
  short: [...hold(10, '1m'), { duration: '15s', target: 0 }],
  steps: [...hold(10, '2m'), ...hold(50, '2m'), ...hold(200, '2m'), ...hold(500, '2m'), { duration: '30s', target: 0 }],
};

export function stages() {
  const name = __ENV.LOADTEST_PROFILE || 'short';
  const profile = PROFILES[name];
  if (!profile) {
    throw new Error(`LOADTEST_PROFILE is "${name}"; use one of: ${Object.keys(PROFILES).join(', ')}`);
  }
  return profile;
}

// Stop a run early when the stack is clearly failing, instead of hammering it to the end:
// more than 5% of requests failing, looked at once the run is 30 seconds old.
export const STOP_WHEN_FAILING = { threshold: 'rate<0.05', abortOnFail: true, delayAbortEval: '30s' };
