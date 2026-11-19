// Mixed: the three scenarios at once, in the proportions a live game would see.
//
// Players do not all do the same thing at the same moment. At any time some are loading a page,
// most are looking at their clan and friends, and some are talking in their clan's chat room.
// This script runs the three scenarios side by side, each with its share of the virtual players:
//
//   widget_open   20%   page loads: widget config, then a player token        (widget-open.js)
//   social_reads  50%   looking around: profile, clan, members, friends        (social-reads.js)
//   clan_chat     30%   in the clan chat room over a WebSocket, writing        (clan-chat.js)
//
// The shares can be changed with LOADTEST_MIX, for example "10,60,30" (widget, reads, chat; they
// are percentages and must add up to 100). The profile (LOADTEST_PROFILE) is the total: with
// "steps", the 500-player step is 100 page-loaders, 250 readers and 150 chatters.
//
// The limits are those of the three scripts, so a crossed limit names the request or the chat
// figure that gave out. Every request is also tagged with its scenario, so the end-of-run summary
// can be split by it (k6 run --summary-trend-stats or the JSON output).
//
//   k6 run loadtest/mixed.js
//   $env:LOADTEST_PROFILE = "steps"; k6 run loadtest/mixed.js
import clanChat, { options as chatOptions } from './clan-chat.js';
import { stages } from './lib/profiles.js';
import { playersFor, seedPlayers } from './lib/seed.js';
import socialReads, { options as readsOptions } from './social-reads.js';
import widgetOpen, { options as widgetOptions } from './widget-open.js';

/** The shares of LOADTEST_MIX ("widget,reads,chat" in percent), checked to add up to 100. */
function mix() {
  const text = __ENV.LOADTEST_MIX || '20,50,30';
  const shares = text.split(',').map((part) => Number(part.trim()));
  const valid = shares.length === 3 && shares.every((share) => Number.isFinite(share) && share >= 0);
  if (!valid || shares.reduce((sum, share) => sum + share, 0) !== 100) {
    throw new Error(`LOADTEST_MIX is "${text}"; give three percentages that add up to 100, for example "20,50,30"`);
  }
  return { widget: shares[0] / 100, reads: shares[1] / 100, chat: shares[2] / 100 };
}

/**
 * The profile's stages with every target scaled to one scenario's share. A share of a step is at
 * least one player, so even the short profile runs all three scenarios.
 */
function scaled(share) {
  return stages().map((stage) => ({
    ...stage,
    target: stage.target === 0 || share === 0 ? 0 : Math.max(1, Math.round(stage.target * share)),
  }));
}

const shares = mix();
const SCENARIOS = {
  widget_open: { share: shares.widget, exec: 'widgetOpen', base: widgetOptions.scenarios.widget_open },
  social_reads: { share: shares.reads, exec: 'socialReads', base: readsOptions.scenarios.social_reads },
  clan_chat: { share: shares.chat, exec: 'clanChat', base: chatOptions.scenarios.clan_chat },
};

export const options = {
  scenarios: Object.fromEntries(
    Object.entries(SCENARIOS)
      .filter(([, scenario]) => scenario.share > 0)
      .map(([name, scenario]) => [name, { ...scenario.base, stages: scaled(scenario.share), exec: scenario.exec }]),
  ),
  // Seeding is the same as for social reads and chat: one prepared player per virtual player.
  setupTimeout: '15m',
  // The three scripts' limits together. They share http_req_failed and checks, which are the same
  // in all three; the rest names one kind of request or one chat figure each.
  thresholds: { ...widgetOptions.thresholds, ...readsOptions.thresholds, ...chatOptions.thresholds },
};

// Every virtual player in the run gets a different test player (exec.vu.idInTest is unique across
// the three scenarios), so a reader and a chatter are never the same player.
export function setup() {
  return { players: seedPlayers(playersFor(stages())) };
}

export { clanChat, socialReads, widgetOpen };
