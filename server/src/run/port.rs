//! Defines the channel boundary between a Run and its adapter.

use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;

use crate::model::RunId;

use super::{RunCommand, RunEvent, RunHandle, RunPhaseControl};

pub struct RunPort {
    pub commands: mpsc::UnboundedReceiver<RunCommand>,
    pub events: mpsc::UnboundedSender<RunEvent>,
    pub(crate) phase: RunPhaseControl,
}

pub struct RunSession {
    pub events: mpsc::UnboundedReceiver<RunEvent>,
}

pub fn channel(run_id: RunId, _capacity: usize) -> (RunPort, RunSession, RunHandle) {
    let (commands_tx, commands_rx) = mpsc::unbounded_channel();
    // Model thinking streams can emit thousands of deltas per call. A bounded
    // channel lets the output adapter starve on runtime actions and deadlock the
    // engine before RunEvent::Ended is delivered.
    let (events_tx, events_rx) = mpsc::unbounded_channel();
    let phase = RunPhaseControl::new();
    let cancellation = CancellationToken::new();
    let handle = RunHandle::new(run_id, phase.clone(), commands_tx, cancellation.clone());
    (
        RunPort {
            commands: commands_rx,
            events: events_tx,
            phase,
        },
        RunSession { events: events_rx },
        handle,
    )
}
