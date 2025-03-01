import { signInAnon, connectionCount, addConnection, insertAnonConnection, leaderboard } from './supabase';

require('dotenv').config({ path: '.env.development' });

const supabaseTestUUID: string = process.env.REACT_APP_SUPABASE_TEST_UUID || '';
if (!supabaseTestUUID) {
    throw new Error('REACT_APP_SUPABASE_TEST_UUID')
}

describe('connectionCount', () => {
    test('should fetch connections successfully', async () => {
        try {
            console.log('Starting connectionCount...');
            const count = await connectionCount();
            console.log('count:', count);
            if (count === 0) {
                throw new Error("failed to fetch connection count")
            } else {
                console.log('connectionCount counted: ', count);
            }
        } catch (error) {
            console.error('Error during connectionCount execution');
            throw error;
        }
    });
});

describe('addConnection', () => {
    test('should add a connection successfully', async () => {
        try {
            console.log('Starting addConnection...');
            const startingCount = await connectionCount();
            if (startingCount === 0) {
                throw new Error('Failed to get starting count')
            }
            console.log('Starting count:', startingCount);

            const result = await addConnection(supabaseTestUUID);
            if (!result.success) {
                throw new Error(`Failed to add connection: ${result.error}`);
            } else {
                // Verify the connection was added
                const currentCount = await connectionCount();
                expect(currentCount).toBeGreaterThan(startingCount)
                if (currentCount > startingCount ) {
                    console.log('Connection count verified successfully');
                } else {
                    throw new Error('Failed to fetch connections after adding');
                }
            }
        } catch (error) {
            console.error('Error during addConnection execution');
            throw error;
        }
    });
});

describe('sign in anon', () => {
    test('should sign in anonymously', async () => {
        try {
            console.log('Starting signInAnon...');
            const result = await signInAnon();
            if (result.success) {
                console.log('Anonymous sign-in successful:', result.data);
            } else {
                throw new Error(`Anonymous sign-in failed: ${result.error}`);
            }
        } catch (error) {
            console.error('Error during signInAnon execution');
            throw error;
        }
    })
});

describe('insertAnonConnection', () => {
    test('should insert an anonymous connection', async () => {
        try {
            console.log('Starting insertAnonConnection...');
            const result = await insertAnonConnection(supabaseTestUUID, 'team_code');
            if (result.success) {
                console.log('Anonymous connection inserted successfully:', result.data);
                // TODO is this correct and sign of success that result.dat == null?
            } else {
                throw new Error(`Failed to insert anonymous connection: ${result.error}`);
            }
        } catch (error) {
            console.error('Error during insertAnonConnection execution');
            throw error;
        }
    })
});

describe('leaderboard', () => {
    test('should fetch the leaderboard', async () => {
        try {
            console.log('Starting to fetch leaderboard...')
            const board = await leaderboard()
            if (board === null) {
                throw new Error('failed to fetch leaderboard')
            }
            console.log('Leaderboard: ', board)
        } catch (error) {
            throw error;
        }
    })
});

