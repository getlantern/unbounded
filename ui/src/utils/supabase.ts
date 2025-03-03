import { createClient, SupabaseClient } from '@supabase/supabase-js';

const supabaseUrl: string = process.env.REACT_APP_SUPABASE_URL || '';
const supabaseKey: string = process.env.REACT_APP_SUPABASE_PUBLIC_KEY || '';
const supabaseTestUUID: string = process.env.REACT_APP_SUPABASE_TEST_UUID || '';

// keys for local storage
export const keyUUID = 'supabase_uuid'; 
export const keyTeamCode= 'team_code'; 

if (!supabaseUrl) {
  throw new Error('REACT_APP_SUPABASE_URL not set');
}
if (!supabaseKey) {
  throw new Error('REACT_APP_SUPABASE_PUBLIC_KEY');
}

const supabase: SupabaseClient = createClient(supabaseUrl, supabaseKey); // TODO: make this async for faster initial page load? 

// lists all rows in connections table
export async function fetchConnections(): Promise<any[] | null> {
  const { data: connections, error } = await supabase
    .from('connections')
    .select('*');
  if (error) {
    console.error('Error fetching connections:', error);
    return null;
  } else {
    return connections;
  }
}

// gets the stored uuid, or creates one with supabase and stores it
async function getUUID(): Promise<string> {
  const storedUUID = localStorage.getItem(keyUUID);
  if (storedUUID) {
    return storedUUID;
  } else {
    console.log('no supabase uuid found, generating one...')
    let { data, error } = await supabase.auth.signInAnonymously()
    if (error) {
      console.error('Error generating UUID:', error);
      return '';
    } 
    if (data && data.user && data.user.id){
      let uuid = data.user.id;
      console.log('generated supabase uuid:', uuid)
      localStorage.setItem(keyUUID, uuid);
      return uuid;
    }else{
      console.error('supabase AuthResponse data.user.id may have been null:', data);
      return '';
    }
  }
}

interface AddConnectionResult {
  success: boolean;
  data?: any[];
  error?: string;
}

export async function addConnection(): Promise<AddConnectionResult> {
  let uuid = await getUUID();
  try {
    const { data, error } = await supabase
      .rpc('record_connection', { user_id_input: uuid });
    if (error) {
      console.error("record_connection failed: ", error);
      return { success: false, error: error.message };
    }
    console.log('Connection inserted successfully: ', data);
    return { success: true, data };
  } catch (err) {
    console.error('Unexpected error: ', err);
    if (err instanceof Error) {
      return { success: false, error: err.message };
    } else {
      return { success: false, error: 'An unknown error occurred' };
    }
  }
}

export async function addUserToTeam(teamInviteCode: string): Promise<boolean> {
  const uuid = await getUUID();
  let { data, error } = await supabase
  .rpc('add_user_to_team', {
    team_invite_code: teamInviteCode, 
    user_id_input: uuid,
  })
  if (error) {
    console.error('Error adding user to team:', error);
    return false;
  }
  if (data) {
    console.log('add_user_to_team response:', data);
    return true;
  }
  console.error('Error adding user to team: No data returned');
  return false;
 }
