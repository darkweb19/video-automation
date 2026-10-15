//! Bounded, nonsemantic scene/motion measurements for the clipping worker.
//! Operates on 2 fps grayscale frames decoded with FFmpeg; prefers NVDEC when
//! available but never assumes CUDA support or silently fabricates results.
//! No model weights, network requests, shell, or third-party Rust crates.

use std::collections::BTreeMap;
use std::env;
use std::io::{self, Read};
use std::process::{Command, Stdio};

const SAMPLE_HZ: u64 = 2;
const WIDTH: usize = 128;
const HEIGHT: usize = 72;
const FRAME_BYTES: usize = WIDTH * HEIGHT;
const MAX_DURATION_MS: u64 = 14_400_000;
const FULL_BIN_MS: u64 = 15_000;
const DENSE_LIMIT: usize = 32;

#[derive(Clone, Copy, PartialEq, Eq)]
enum Mode { Full, Dense }

#[derive(Clone)]
struct Event {
    kind: &'static str,
    start: u64,
    end: u64,
    mad: f64,
    histogram: f64,
    score: f64,
}

fn argument(args: &[String], key: &str) -> Result<String, &'static str> {
    let mut found = None;
    for pair in args.windows(2) {
        if pair[0] == key {
            if found.is_some() { return Err("duplicate argument"); }
            found = Some(pair[1].clone());
        }
    }
    found.ok_or("missing argument")
}

fn number(args: &[String], key: &str) -> Result<u64, &'static str> {
    argument(args, key)?.parse::<u64>().map_err(|_| "invalid numeric argument")
}

fn histogram(frame: &[u8]) -> [u32; 32] {
    let mut h = [0u32; 32];
    for pixel in frame { h[usize::from(*pixel) / 8] += 1; }
    h
}

fn differences(a: &[u8], b: &[u8], left: &[u32; 32], right: &[u32; 32]) -> (f64, f64) {
    let absolute: u64 = a.iter().zip(b).map(|(x, y)| (i32::from(*x) - i32::from(*y)).unsigned_abs() as u64).sum();
    let distribution: u64 = left.iter().zip(right).map(|(x, y)| (i64::from(*x) - i64::from(*y)).unsigned_abs()).sum();
    (absolute as f64 / FRAME_BYTES as f64, distribution as f64 / (2.0 * FRAME_BYTES as f64))
}

fn add_full(map: &mut BTreeMap<(u64, u8), Event>, event: Event) {
    let category = if event.kind == "scene_change" { 0 } else { 1 };
    // A single strongest event of each kind per 15-second source bin preserves
    // coverage across four-hour sources without flooding the callback artifact.
    let key = (event.start / FULL_BIN_MS, category);
    if map.get(&key).is_none_or(|prior| event.score > prior.score) {
        map.insert(key, event);
    }
}

fn emit(event: &Event, dense: bool) -> String {
    if dense {
        format!(
            r#"{{"start_ms":{},"end_ms":{},"kind":"dense_motion_change","score":{:.4},"evidence":{{"detector":"rust_gray_frame_difference_v1","mean_absolute_difference":{:.2},"histogram_distance":{:.4},"sample_rate_hz":2,"visual_evidence_strength":"weak"}}}}"#,
            event.start, event.end, event.score, event.mad, event.histogram
        )
    } else if event.kind == "scene_change" {
        format!(
            r#"{{"kind":"scene_change","start_ms":{},"end_ms":{},"detector":"rust_gray_histogram_cut_v1","histogram_distance":{:.4},"visual_evidence_strength":"weak"}}"#,
            event.start, event.end, event.histogram
        )
    } else {
        format!(
            r#"{{"kind":"motion_change","start_ms":{},"end_ms":{},"detector":"rust_gray_frame_difference_v1","mean_absolute_difference":{:.2},"visual_evidence_strength":"weak"}}"#,
            event.start, event.end, event.mad
        )
    }
}

fn analyze_frames(
    pipe: &mut impl Read,
    mode: Mode,
    duration: u64,
    start: u64,
    end: u64,
) -> Result<(usize, Vec<Event>), &'static str> {
    let mut current = vec![0u8; FRAME_BYTES];
    let mut previous = vec![0u8; FRAME_BYTES];
    let mut old_hist = [0u32; 32];
    let mut count = 0usize;
    let mut chosen = BTreeMap::new();
    let mut dense_events = Vec::new();
    let max_frames = (((end - start) as u128 * SAMPLE_HZ as u128 + 999) / 1000 + 2) as usize;

    loop {
        let mut read = 0;
        while read < FRAME_BYTES {
            match pipe.read(&mut current[read..]) {
                Ok(0) if read == 0 => break,
                Ok(0) => return Err("decoder emitted partial frame"),
                Ok(n) => read += n,
                Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
                Err(_) => return Err("decoder read failed"),
            }
        }
        if read == 0 { break; }
        count += 1;
        if count > max_frames || (mode == Mode::Dense && count > 360) {
            return Err("decoded frame count exceeds sampling bound");
        }
        if count > 1 {
            let time = start + (count as u64 - 1) * 1000 / SAMPLE_HZ;
            if time >= end { break; }
            let next_hist = histogram(&current);
            let (mad, hist_diff) = differences(&current, &previous, &next_hist, &old_hist);
            let score = ((mad / 255.0) * 0.65 + hist_diff * 0.35).clamp(0.0, 1.0);
            if mode == Mode::Dense {
                if mad >= 16.0 {
                    dense_events.push(Event {
                        kind: "dense_motion_change",
                        start: time,
                        end: (time + 500).min(end),
                        mad,
                        histogram: hist_diff,
                        score: mad / 255.0,
                    });
                }
            } else {
                if (hist_diff >= 0.30 && mad >= 15.0) || (mad >= 60.0 && hist_diff >= 0.13) {
                    add_full(&mut chosen, Event {
                        kind: "scene_change", start: time, end: (time + 1).min(end),
                        mad, histogram: hist_diff, score,
                    });
                }
                if mad >= 22.0 {
                    add_full(&mut chosen, Event {
                        kind: "motion_change", start: time, end: (time + 500).min(end),
                        mad, histogram: hist_diff, score,
                    });
                }
            }
            old_hist = next_hist;
        } else {
            old_hist = histogram(&current);
        }
        std::mem::swap(&mut current, &mut previous);
    }
    if count == 0 { return Err("decoder returned no frames"); }
    if mode == Mode::Dense {
        dense_events.sort_by(|a, b| b.score.total_cmp(&a.score).then(a.start.cmp(&b.start)));
        dense_events.truncate(DENSE_LIMIT);
        dense_events.sort_by_key(|e| e.start);
        Ok((count, dense_events))
    } else {
        let mut events: Vec<Event> = chosen.into_values().collect();
        events.sort_by(|a, b| a.start.cmp(&b.start).then(a.kind.cmp(b.kind)));
        Ok((count, events))
    }
}

fn decode(
    input: &str, duration: u64, start: u64, end: u64, mode: Mode, cuda: bool
) -> Result<(usize, Vec<Event>), &'static str> {
    let mut cmd = Command::new("ffmpeg");
    cmd.arg("-nostdin").args(["-hide_banner", "-loglevel", "error"]);
    if cuda { cmd.args(["-hwaccel", "cuda"]); }
    if start > 0 { cmd.arg("-ss").arg(format!("{:.3}", start as f64 / 1000.0)); }
    cmd.args(["-protocol_whitelist", "file,crypto,data"]);
    cmd.args(["-format_whitelist", "mov,matroska,webm"]);
    cmd.arg("-i").arg(input);
    cmd.arg("-t").arg(format!("{:.3}", (end - start) as f64 / 1000.0));
    cmd.args(["-an", "-sn", "-dn", "-vf", "fps=2,scale=128:72:flags=fast_bilinear,format=gray"]);
    cmd.args(["-f", "rawvideo", "-pix_fmt", "gray", "pipe:1"]);
    cmd.stdin(Stdio::null()).stdout(Stdio::piped()).stderr(Stdio::null());
    let mut child = cmd.spawn().map_err(|_| "ffmpeg could not start")?;
    let outcome = match child.stdout.take() {
        Some(mut stdout) => analyze_frames(&mut stdout, mode, duration, start, end),
        None => Err("ffmpeg stdout is unavailable"),
    };
    if outcome.is_err() { let _ = child.kill(); }
    let status = child.wait().map_err(|_| "ffmpeg wait failed")?;
    if !status.success() { return Err("ffmpeg decode failed"); }
    outcome
}

fn main_result() -> Result<String, &'static str> {
    let args: Vec<String> = env::args().skip(1).collect();
    if args.len() != 12 { return Err("expected six argument pairs"); }
    let input = argument(&args, "--input")?;
    if input.is_empty() || !std::path::Path::new(&input).is_file() { return Err("source file missing"); }
    let duration = number(&args, "--duration-ms")?;
    let start = number(&args, "--start-ms")?;
    let end = number(&args, "--end-ms")?;
    if duration == 0 || duration > MAX_DURATION_MS || start >= end || end > duration { return Err("invalid video window"); }
    let mode = match argument(&args, "--mode")?.as_str() {
        "full" if start == 0 && end == duration => Mode::Full,
        "dense" if end - start <= 180_000 => Mode::Dense,
        _ => return Err("invalid mode or window"),
    };
    let preference = argument(&args, "--decode")?;
    if !matches!(preference.as_str(), "auto" | "software" | "cuda") { return Err("invalid decoder preference"); }
    let (frames, events, backend) = if preference == "software" {
        let (n, events) = decode(&input, duration, start, end, mode, false)?;
        (n, events, "software")
    } else {
        match decode(&input, duration, start, end, mode, true) {
            Ok((n, events)) => (n, events, "cuda"),
            Err(_) if preference == "auto" => {
                let (n, events) = decode(&input, duration, start, end, mode, false)?;
                (n, events, "software")
            }
            Err(reason) => return Err(reason),
        }
    };
    let result_events = events.iter().map(|e| emit(e, mode == Mode::Dense)).collect::<Vec<_>>().join(",");
    let output = if mode == Mode::Dense {
        format!(
            r#"{{"engine":"rust-scene-v1","decoder":"{}","sample_rate_hz":2,"sampled_frames":{},"visual_events":[{}]}}"#,
            backend, frames, result_events
        )
    } else {
        format!(
            r#"{{"engine":"rust-scene-v1","decoder":"{}","covered_duration_ms":{},"events":[{}]}}"#,
            backend, duration, result_events
        )
    };
    Ok(output)
}

fn main() {
    match main_result() {
        Ok(output) => println!("{output}"),
        Err(reason) => {
            // Static diagnostics only: no paths, tokens, URLs or transcript in logs.
            eprintln!("framevault_rust_scene_failed reason={reason}");
            std::process::exit(1);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn histogram_and_activity_are_bounded() {
        let black = vec![0u8; FRAME_BYTES];
        let white = vec![255u8; FRAME_BYTES];
        let (mad, histogram_delta) = differences(
            &black, &white, &histogram(&black), &histogram(&white)
        );
        assert_eq!(mad, 255.0);
        assert_eq!(histogram_delta, 1.0);
    }
    #[test]
    fn event_bucket_keeps_strongest_signal() {
        let mut bins = BTreeMap::new();
        for (start, score) in [(1_000, 0.5), (2_000, 0.8), (30_000, 0.2)] {
            add_full(&mut bins, Event {
                kind: "motion_change", start, end: start + 500, mad: 23.0,
                histogram: 0.1, score
            });
        }
        assert_eq!(bins.len(), 2);
        assert_eq!(bins[&(0, 1)].start, 2_000);
    }
    #[test]
    fn dense_frames_produce_bounded_events() {
        let black = vec![0u8; FRAME_BYTES];
        let white = vec![255u8; FRAME_BYTES];
        let stream = [black.clone(), white, black].concat();
        let (_, events) = analyze_frames(&mut stream.as_slice(), Mode::Dense, 2000, 0, 2000).unwrap();
        assert_eq!(events.len(), 2);
        assert_eq!(events[0].start, 500);
    }
}
