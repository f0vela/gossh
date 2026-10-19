mod app;
mod ssh;
mod store;
mod ui;

use anyhow::Result;

use crate::app::App;
use crate::store::SecureStore;

fn main() -> Result<()> {
    let store = SecureStore::new()?;
    let loaded = store.load_profiles()?;
    let mut app = App::new(store, loaded.profiles);
    if let Some(notice) = loaded.notice {
        app.set_startup_notice(notice);
    }
    app.run()
}
