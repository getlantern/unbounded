// TODO: this is a standalone file and needs to be integrated with the rest of the ui to be useful

const { createClient, SupabaseClient } = require('@supabase/supabase-js');
require('dotenv').config({ path: '.env' });

const supabaseUrl = process.env.SUPABASE_URL
const supabaseKey = process.env.SUPABASE_SECRET 
console.log(supabaseKey)
const supabase = createClient(supabaseUrl, supabaseKey)

// lists all rows in connections table
async function fetchConnections() {
    let { data: connections, error } = await supabase
      .from('connections')
      .select('*')
  
    if (error) {
      console.error('Error fetching connections:', error)
    } else {
      console.log('Connections:', connections)
    }
  }
  fetchConnections()


  // inserts to the connections table using the record_connection function
  const testUUID = 'some_uuid'  // replace with actual UUID
  async function insertConnection() {
    let { data, error } = await supabase
    .rpc('record_connection', {"user_id_input": testUUID})
    if (error) console.error(error)
    else console.log(data)
  }
  insertConnection()

  async function signInAnon() {
    let { data, error } = await supabase.auth.signInAnonymously()
    if (error) {
      console.error('Error with anon sign in:', error)
    } else {
      console.log('signed in:', data)
    }
  }

    // inserts to the connections table using the record_connection function
    async function insertAnonConnection(input_uuid, team_code) {
        let { data, error } = await supabase
        .rpc('record_anon_connection', {
            "user_id_input": input_uuid,
            "team_code_input": team_code,
    
        })
        if (error) console.error(error)
        else console.log(data)
      }
    
    async function record_anon_user(team_code) {
        data = await signInAnon()
        UUID = data.user.id
        console.log('UUID:', UUID)
    
        insertAnonConnection(UUID, team_code)
    }
    
    //can call this to record a connection with a team code, which could be saved in the browser extension settings 
    record_anon_user('join_pikas_team')
    