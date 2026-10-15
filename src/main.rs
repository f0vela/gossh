mod app;
mod ssh;
mod store;
mod ui;

use anyhow::Result;

use crate::app::App;
use crate::store::SecureStore;

fn main() -> Result<()> {
    let store = SecureStore::new()?;
    let profiles = store.load_profiles()?;
    let mut app = App::new(store, profiles);
    app.run()
}
